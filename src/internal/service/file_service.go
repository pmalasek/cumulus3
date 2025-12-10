package service

import (
	"bytes"
	"compress/gzip"
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
	mimeType := s.determineMimeType(filename, contentType)

	result, err := s.processStream(file)
	if err != nil {
		return "", err
	}
	defer result.cleanup()

	finalFile, sizeCompressed, alg := s.decideCompression(result)

	blobID, err := s.saveBlob(result.hash, finalFile, result.sizeRaw, sizeCompressed, alg, mimeType)
	if err != nil {
		return "", err
	}

	return s.saveFile(filename, blobID, oldCumulusID, expiresAt, tags)
}

// DownloadFile retrieves a file by its ID, handling decompression if necessary
func (s *FileService) DownloadFile(fileID string) ([]byte, string, string, error) {
	file, err := s.MetaStore.GetFile(fileID)
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

	return decompressedData, file.Name, fileType.MimeType, nil
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

	switch s.CompressionMode {
	case "Gzip":
		shouldCompress = true
		compressionAlg = "gzip"
	case "Zstd":
		shouldCompress = true
		compressionAlg = "zstd"
	case "Auto":
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
func (s *FileService) saveBlob(hash string, file *os.File, sizeRaw, sizeCompressed int64, alg, mimeType string) (int64, error) {
	// 1. Check if blob exists
	blobID, exists, err := s.MetaStore.GetBlobIDByHash(hash)
	if err != nil {
		return 0, fmt.Errorf("database error: %w", err)
	}
	if exists {
		return blobID, nil
	}

	// 2. Create new blob record to get ID
	blobID, err = s.MetaStore.CreateBlob(hash)
	if err != nil {
		return 0, fmt.Errorf("database error creating blob: %w", err)
	}

	// 3. Write to storage
	file.Seek(0, 0)
	data, err := io.ReadAll(file)
	if err != nil {
		return 0, fmt.Errorf("error reading file for storage: %w", err)
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
		return 0, fmt.Errorf("storage error: %w", err)
	}

	// 4. Update blob location metadata
	category := "unknown"
	subtype := "unknown"
	if parts := strings.Split(mimeType, "/"); len(parts) == 2 {
		category = parts[0]
		subtype = parts[1]
	}

	fileTypeID, err := s.MetaStore.GetOrCreateFileType(mimeType, category, subtype)
	if err != nil {
		return 0, fmt.Errorf("metadata error: %w", err)
	}

	err = s.MetaStore.UpdateBlobLocation(blobID, volID, offset, sizeRaw, sizeCompressed, alg, fileTypeID)
	if err != nil {
		return 0, fmt.Errorf("database error updating blob: %w", err)
	}

	return blobID, nil
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
