package service

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/pmalasek/cumulus3/src/internal/storage"
	"github.com/pmalasek/cumulus3/src/internal/utils"
	"golang.org/x/crypto/blake2b"
)

type FileService struct {
	Store               *storage.Store
	MetaStore           *storage.MetadataSQL
	Logger              *storage.MetadataLogger
	CompressionMode     string
	MinCompressionRatio float64
}

// NewFileService creates a new instance of FileService
func NewFileService(store *storage.Store, metaStore *storage.MetadataSQL, logger *storage.MetadataLogger, compressionMode string, minCompressionRatio float64) *FileService {
	return &FileService{
		Store:               store,
		MetaStore:           metaStore,
		Logger:              logger,
		CompressionMode:     compressionMode,
		MinCompressionRatio: minCompressionRatio,
	}
}

// UploadFile handles the entire file upload process: streaming, compression, deduplication, and metadata storage
func (s *FileService) UploadFile(file io.Reader, filename string, contentType string, oldCumulusID *int64, expiresAt *time.Time, tags string) (string, error) {
	id, _, err := s.UploadFileWithDedup(file, filename, contentType, oldCumulusID, expiresAt, tags)
	return id, err
}

// UploadFileWithDedup handles the entire file upload process and returns deduplication status
func (s *FileService) UploadFileWithDedup(file io.Reader, filename string, contentType string, oldCumulusID *int64, expiresAt *time.Time, tags string) (string, bool, error) {
	result, err := s.processStream(file)
	if err != nil {
		return "", false, err
	}
	defer result.cleanup()

	// Detect file type
	// Read first 12KB for detection
	detectBuffer := make([]byte, 12000)
	result.tempFile.Seek(0, 0)
	n, _ := io.ReadFull(result.tempFile, detectBuffer)
	fileType := utils.DetectFileType(detectBuffer[:n])
	utils.Info("SERVICE", " File type detected: type=%s, subtype=%s, mime=%s, hash=%s",
		fileType.Type, fileType.Subtype, fileType.ContentType, result.hash)

	// If detection returned generic binary, try to use provided content type or extension
	if fileType.Type == "binary" && fileType.Subtype == "" {
		mimeType := s.determineMimeType(filename, contentType)
		if mimeType != "application/octet-stream" {
			fileType.ContentType = mimeType
			// Try to guess category/subtype from mimeType
			parts := strings.Split(mimeType, "/")
			if len(parts) == 2 {
				fileType.Type = parts[0]
				fileType.Subtype = parts[1]
			}
		}
	}

	finalFile, sizeCompressed, alg := s.decideCompression(result)
	utils.Info("SERVICE", " Compression decision: raw_size=%d, compressed_size=%d, algorithm=%s, hash=%s",
		result.sizeRaw, sizeCompressed, alg, result.hash)

	blobID, isDedup, err := s.saveBlob(result.hash, finalFile, result.sizeRaw, sizeCompressed, alg, fileType)
	if err != nil {
		utils.Info("SERVICE", " ERROR saving blob: hash=%s, error=%v", result.hash, err)
		return "", false, err
	}

	if isDedup {
		utils.Info("SERVICE", " Deduplication hit: hash=%s, blob_id=%d", result.hash, blobID)
	}

	fileID, err := s.saveFile(filename, blobID, oldCumulusID, expiresAt, tags)
	if err != nil {
		utils.Info("SERVICE", " ERROR saving file metadata: filename=%s, blob_id=%d, error=%v", filename, blobID, err)
	}
	return fileID, isDedup, err
}

// DownloadFile retrieves a file by its ID, handling decompression if necessary
func (s *FileService) DownloadFile(fileID string) ([]byte, string, string, error) {
	file, err := s.MetaStore.GetFile(fileID)
	if err != nil {
		utils.Info("SERVICE", " File not found in metadata: file_id=%s, error=%v", fileID, err)
		return nil, "", "", fmt.Errorf("file not found: %w", err)
	}

	blob, err := s.MetaStore.GetBlob(file.BlobID)
	if err != nil {
		utils.Info("SERVICE", " Blob not found: file_id=%s, blob_id=%d, error=%v", fileID, file.BlobID, err)
		return nil, "", "", fmt.Errorf("blob not found: %w", err)
	}

	fileType, err := s.MetaStore.GetFileType(blob.FileTypeID)
	if err != nil {
		utils.Info("SERVICE", " FileType not found: file_id=%s, file_type_id=%d, error=%v", fileID, blob.FileTypeID, err)
		return nil, "", "", fmt.Errorf("file type not found: %w", err)
	}

	utils.Info("SERVICE", " FileType from DB: file_id=%s, mime=%s, category=%s, subtype=%s", fileID, fileType.MimeType, fileType.Category, fileType.Subtype)
	utils.Info("SERVICE", " Reading blob: file_id=%s, blob_id=%d, volume_id=%d, offset=%d, size=%d, compression=%s",
		fileID, file.BlobID, blob.VolumeID, blob.Offset, blob.SizeCompressed, blob.CompressionAlg)

	data, err := s.Store.ReadBlob(blob.VolumeID, blob.Offset, blob.SizeCompressed)
	if err != nil {
		utils.Info("SERVICE", " ERROR reading blob from storage: file_id=%s, blob_id=%d, volume=%d, offset=%d, size=%d, error=%v",
			fileID, file.BlobID, blob.VolumeID, blob.Offset, blob.SizeCompressed, err)
		return nil, "", "", fmt.Errorf("error reading blob: %w", err)
	}

	// Decompress if needed
	var decompressedData []byte
	switch blob.CompressionAlg {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", "", fmt.Errorf("gzip error: %w", err)
		}
		defer reader.Close()
		decompressedData, err = io.ReadAll(reader)
		if err != nil {
			return nil, "", "", fmt.Errorf("gzip read error: %w", err)
		}
	case "zstd":
		decoder, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", "", fmt.Errorf("zstd error: %w", err)
		}
		defer decoder.Close()
		decompressedData, err = io.ReadAll(decoder)
		if err != nil {
			return nil, "", "", fmt.Errorf("zstd read error: %w", err)
		}
	case "none", "":
		decompressedData = data
	default:
		return nil, "", "", fmt.Errorf("unknown compression algorithm: %s", blob.CompressionAlg)
	}

	// Fallback if mime type is empty
	mimeType := fileType.MimeType
	if mimeType == "" {
		mimeType = s.determineMimeType(file.Name, "")
		utils.Info("SERVICE", " Empty mime type from DB, using fallback: file_id=%s, fallback_mime=%s", fileID, mimeType)
	}

	return decompressedData, file.Name, mimeType, nil
}

// DownloadFileByOldID retrieves a file by its old Cumulus ID
func (s *FileService) DownloadFileByOldID(oldID int64) ([]byte, string, string, error) {
	file, err := s.MetaStore.GetFileByOldID(oldID)
	if err != nil {
		return nil, "", "", fmt.Errorf("file not found: %w", err)
	}

	blob, err := s.MetaStore.GetBlob(file.BlobID)
	if err != nil {
		return nil, "", "", fmt.Errorf("blob not found: %w", err)
	}

	fileType, err := s.MetaStore.GetFileType(blob.FileTypeID)
	if err != nil {
		return nil, "", "", fmt.Errorf("file type not found: %w", err)
	}

	data, err := s.Store.ReadBlob(blob.VolumeID, blob.Offset, blob.SizeCompressed)
	if err != nil {
		return nil, "", "", fmt.Errorf("error reading blob: %w", err)
	}

	// Decompress if needed
	var decompressedData []byte
	switch blob.CompressionAlg {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", "", fmt.Errorf("gzip error: %w", err)
		}
		defer reader.Close()
		decompressedData, err = io.ReadAll(reader)
		if err != nil {
			return nil, "", "", fmt.Errorf("gzip read error: %w", err)
		}
	case "zstd":
		decoder, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", "", fmt.Errorf("zstd error: %w", err)
		}
		defer decoder.Close()
		decompressedData, err = io.ReadAll(decoder)
		if err != nil {
			return nil, "", "", fmt.Errorf("zstd read error: %w", err)
		}
	case "none", "":
		decompressedData = data
	default:
		return nil, "", "", fmt.Errorf("unknown compression algorithm: %s", blob.CompressionAlg)
	}

	// Fallback if mime type is empty
	mimeType := fileType.MimeType
	if mimeType == "" {
		mimeType = s.determineMimeType(file.Name, "")
		utils.Info("SERVICE", " Empty mime type from DB, using fallback: old_id=%d, fallback_mime=%s", oldID, mimeType)
	}

	return decompressedData, file.Name, mimeType, nil
}

// determineMimeType tries to detect the MIME type from Content-Type header or filename extension
func (s *FileService) determineMimeType(filename, contentType string) string {
	if contentType != "" {
		return contentType
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		return "application/octet-stream"
	}
	return mimeType
}

type streamResult struct {
	tempFile           *os.File
	tempCompressedFile *os.File
	hash               string
	sizeRaw            int64
	autoCompress       bool
	forcedAlg          string
}

// cleanup removes temporary files created during the upload process
func (r *streamResult) cleanup() {
	if r.tempFile != nil {
		r.tempFile.Close()
		os.Remove(r.tempFile.Name())
	}
	if r.tempCompressedFile != nil {
		r.tempCompressedFile.Close()
		os.Remove(r.tempCompressedFile.Name())
	}
}

// processStream reads the input stream, calculates hash, and creates temporary files (raw and optionally compressed)
func (s *FileService) processStream(file io.Reader) (*streamResult, error) {
	res := &streamResult{}

	// Decide compression strategy
	shouldCompress := false
	compressionAlg := "none"

	switch strings.ToLower(s.CompressionMode) {
	case "gzip":
		shouldCompress = true
		compressionAlg = "gzip"
	case "zstd":
		shouldCompress = true
		compressionAlg = "zstd"
	case "auto":
		res.autoCompress = true
		compressionAlg = "zstd"
	}
	res.forcedAlg = compressionAlg

	// Create temp files
	var err error
	res.tempFile, err = os.CreateTemp("", "upload-raw-*")
	if err != nil {
		return nil, fmt.Errorf("internal error creating temp file: %w", err)
	}

	// If we fail later, we must clean up
	success := false
	defer func() {
		if !success {
			res.cleanup()
		}
	}()

	if res.autoCompress {
		res.tempCompressedFile, err = os.CreateTemp("", "upload-comp-*")
		if err != nil {
			return nil, fmt.Errorf("internal error creating temp compressed file: %w", err)
		}
	}

	// Setup writers
	hasher, _ := blake2b.New256(nil)
	var writers []io.Writer
	writers = append(writers, hasher)

	var zstdEncoder *zstd.Encoder
	var gzipWriter *gzip.Writer

	if res.autoCompress {
		writers = append(writers, res.tempFile)
		zstdEncoder, _ = zstd.NewWriter(res.tempCompressedFile)
		writers = append(writers, zstdEncoder)
	} else if shouldCompress {
		switch compressionAlg {
		case "gzip":
			gzipWriter = gzip.NewWriter(res.tempFile)
			writers = append(writers, gzipWriter)
		case "zstd":
			zstdEncoder, _ = zstd.NewWriter(res.tempFile)
			writers = append(writers, zstdEncoder)
		}
	} else {
		writers = append(writers, res.tempFile)
	}

	// Copy
	multiW := io.MultiWriter(writers...)
	res.sizeRaw, err = io.Copy(multiW, file)
	if err != nil {
		return nil, fmt.Errorf("error processing file: %w", err)
	}

	// Close compressors
	if zstdEncoder != nil {
		zstdEncoder.Close()
	}
	if gzipWriter != nil {
		gzipWriter.Close()
	}

	// Sync
	res.tempFile.Sync()
	if res.tempCompressedFile != nil {
		res.tempCompressedFile.Sync()
	}

	res.hash = hex.EncodeToString(hasher.Sum(nil))
	success = true
	return res, nil
}

// decideCompression chooses between the raw and compressed file based on the compression ratio (in Auto mode)
func (s *FileService) decideCompression(res *streamResult) (*os.File, int64, string) {
	if res.autoCompress {
		statRaw, _ := res.tempFile.Stat()
		statComp, _ := res.tempCompressedFile.Stat()

		sizeRaw := statRaw.Size()
		sizeCompressed := statComp.Size()

		savedPercent := (float64(sizeRaw-sizeCompressed) / float64(sizeRaw)) * 100

		if savedPercent >= s.MinCompressionRatio {
			return res.tempCompressedFile, sizeCompressed, "zstd"
		}
		return res.tempFile, sizeRaw, "none"
	}

	// Not auto
	stat, _ := res.tempFile.Stat()
	sizeCompressed := stat.Size()

	if res.forcedAlg != "none" {
		return res.tempFile, sizeCompressed, res.forcedAlg
	}
	return res.tempFile, sizeCompressed, "none"
}

// saveBlob stores the file content in the volume storage if it doesn't exist yet (deduplication)
func (s *FileService) saveBlob(hash string, file *os.File, sizeRaw, sizeCompressed int64, alg string, fileType utils.FileTypeResult) (int64, bool, error) {
	// 1. Check if blob exists
	blobID, exists, err := s.MetaStore.GetBlobIDByHash(hash)
	if err != nil {
		return 0, false, fmt.Errorf("database error: %w", err)
	}
	if exists {
		// Check if blob is valid (not a zombie)
		currentBlob, err := s.MetaStore.GetBlob(blobID)
		if err == nil {
			if currentBlob.VolumeID == 0 {
				// Zombie blob detected (created but not uploaded).
				// Treat as if it doesn't exist, but reuse the ID.
				exists = false
			} else {
				// Check if we can improve file type info
				currentFileType, err := s.MetaStore.GetFileType(currentBlob.FileTypeID)
				if err == nil {
					// If current type is generic binary/octet-stream and new type is more specific
					if currentFileType.Category == "binary" && currentFileType.Subtype == "" && fileType.Type != "binary" {
						// Update file type
						newFileTypeID, err := s.MetaStore.GetOrCreateFileType(fileType.ContentType, fileType.Type, fileType.Subtype)
						if err == nil {
							s.MetaStore.UpdateBlobFileType(blobID, newFileTypeID)
						}
					}
				}
				return blobID, true, nil
			}
		}
	}

	// 2. Create new blob record to get ID
	if blobID == 0 {
		blobID, err = s.MetaStore.CreateBlob(hash)
		if err != nil {
			// Handle race condition: another process might have created the blob
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				var exists bool
				var lookupErr error
				blobID, exists, lookupErr = s.MetaStore.GetBlobIDByHash(hash)
				if lookupErr == nil && exists {
					// Successfully recovered from race condition
					// Check if it's a zombie (race condition with a zombie?)
					// If it is a zombie, we have the ID now, so we can proceed to overwrite it.
					// If it is valid, we should return it.

					// For simplicity, if we recover an ID, we assume it's valid or we overwrite it.
					// But if it's valid, we shouldn't overwrite it!

					// Let's check validity again
					recoveredBlob, err := s.MetaStore.GetBlob(blobID)
					if err == nil && recoveredBlob.VolumeID > 0 {
						return blobID, true, nil
					}
					// If it's a zombie, we proceed to upload using this blobID
				}
			}
			if blobID == 0 {
				return 0, false, fmt.Errorf("database error creating blob: %w", err)
			}
		}
	}

	// 3. Write to storage
	file.Seek(0, 0)
	data, err := io.ReadAll(file)
	if err != nil {
		return 0, false, fmt.Errorf("error reading file for storage: %w", err)
	}

	compAlgCode := uint8(0)
	switch alg {
	case "gzip":
		compAlgCode = 1
	case "zstd":
		compAlgCode = 2
	}

	volID, offset, err := s.Store.WriteBlob(blobID, data, compAlgCode)
	if err != nil {
		return 0, false, fmt.Errorf("storage error: %w", err)
	}

	// 4. Update blob location metadata
	fileTypeID, err := s.MetaStore.GetOrCreateFileType(fileType.ContentType, fileType.Type, fileType.Subtype)
	if err != nil {
		return 0, false, fmt.Errorf("metadata error: %w", err)
	}

	err = s.MetaStore.UpdateBlobLocation(blobID, volID, offset, sizeRaw, sizeCompressed, alg, fileTypeID)
	if err != nil {
		return 0, false, fmt.Errorf("database error updating blob: %w", err)
	}

	return blobID, false, nil
}

// saveFile creates a new file record in the metadata database linked to the blob
func (s *FileService) saveFile(filename string, blobID int64, oldCumulusID *int64, expiresAt *time.Time, tags string) (string, error) {
	fileID := uuid.New().String()
	fileMeta := storage.File{
		ID:           fileID,
		Name:         filename,
		BlobID:       blobID,
		OldCumulusID: oldCumulusID,
		ExpiresAt:    expiresAt,
		CreatedAt:    time.Now(),
		Tags:         tags,
	}

	if err := s.MetaStore.SaveFile(fileMeta); err != nil {
		return "", fmt.Errorf("metadata error: %w", err)
	}

	// Log for disaster recovery
	if s.Logger != nil {
		if err := s.Logger.LogFile(fileMeta); err != nil {
			// Log error but don't fail the request
			fmt.Fprintf(os.Stderr, "Failed to write to metadata log: %v\n", err)
		}
	}

	return fileID, nil
}

type FileInfo struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	BlobID         int64      `json:"blob_id"`
	OldCumulusID   *int64     `json:"old_cumulus_id,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	Tags           []string   `json:"tags,omitempty"`
	Hash           string     `json:"hash"`
	SizeRaw        int64      `json:"size_raw"`
	SizeCompressed int64      `json:"size_compressed"`
	CompressionAlg string     `json:"compression_alg"`
	MimeType       string     `json:"mime_type"`
	Category       string     `json:"category"`
	Subtype        string     `json:"subtype"`
	Content        string     `json:"content,omitempty"` // Base64 encoded
}

// GetFileInfo retrieves complete information about a file
func (s *FileService) GetFileInfo(fileID string, extended bool) (*FileInfo, error) {
	file, err := s.MetaStore.GetFile(fileID)
	if err != nil {
		return nil, err
	}

	blob, err := s.MetaStore.GetBlob(file.BlobID)
	if err != nil {
		return nil, err
	}

	fileType, err := s.MetaStore.GetFileType(blob.FileTypeID)
	if err != nil {
		return nil, err
	}

	var tags []string
	if file.Tags != "" {
		tags = strings.Split(file.Tags, ",")
	}

	info := &FileInfo{
		ID:             file.ID,
		Name:           file.Name,
		BlobID:         file.BlobID,
		OldCumulusID:   file.OldCumulusID,
		ExpiresAt:      file.ExpiresAt,
		CreatedAt:      file.CreatedAt,
		Tags:           tags,
		Hash:           blob.Hash,
		SizeRaw:        blob.SizeRaw,
		SizeCompressed: blob.SizeCompressed,
		CompressionAlg: blob.CompressionAlg,
		MimeType:       fileType.MimeType,
		Category:       fileType.Category,
		Subtype:        fileType.Subtype,
	}

	if extended {
		data, _, _, err := s.DownloadFile(fileID)
		if err != nil {
			return nil, err
		}
		info.Content = base64.StdEncoding.EncodeToString(data)
	}

	return info, nil
}

// GetFileInfoByOldID retrieves complete information about a file by its old Cumulus ID
func (s *FileService) GetFileInfoByOldID(oldID int64, extended bool) (*FileInfo, error) {
	file, err := s.MetaStore.GetFileByOldID(oldID)
	if err != nil {
		return nil, err
	}

	blob, err := s.MetaStore.GetBlob(file.BlobID)
	if err != nil {
		return nil, err
	}

	fileType, err := s.MetaStore.GetFileType(blob.FileTypeID)
	if err != nil {
		return nil, err
	}

	var tags []string
	if file.Tags != "" {
		tags = strings.Split(file.Tags, ",")
	}

	info := &FileInfo{
		ID:             file.ID,
		Name:           file.Name,
		BlobID:         file.BlobID,
		OldCumulusID:   file.OldCumulusID,
		ExpiresAt:      file.ExpiresAt,
		CreatedAt:      file.CreatedAt,
		Tags:           tags,
		Hash:           blob.Hash,
		SizeRaw:        blob.SizeRaw,
		SizeCompressed: blob.SizeCompressed,
		CompressionAlg: blob.CompressionAlg,
		MimeType:       fileType.MimeType,
		Category:       fileType.Category,
		Subtype:        fileType.Subtype,
	}

	if extended {
		data, _, _, err := s.DownloadFileByOldID(oldID)
		if err != nil {
			return nil, err
		}
		info.Content = base64.StdEncoding.EncodeToString(data)
	}

	return info, nil
}

// DeleteFile deletes a file and updates storage stats
func (s *FileService) DeleteFile(fileID string) error {
	return s.MetaStore.DeleteFile(fileID)
}
