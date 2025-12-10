package service

import (
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
	CompressionMode     string
	MinCompressionRatio float64
}

// NewFileService creates a new instance of FileService
func NewFileService(store *storage.Store, metaStore *storage.MetadataSQL, compressionMode string, minCompressionRatio float64) *FileService {
	return &FileService{
		Store:               store,
		MetaStore:           metaStore,
		CompressionMode:     compressionMode,
		MinCompressionRatio: minCompressionRatio,
	}
}

// UploadFile handles the entire file upload process: streaming, compression, deduplication, and metadata storage
func (s *FileService) UploadFile(file io.Reader, filename string, contentType string, oldCumulusID *int64, expiresAt *time.Time) (string, error) {
	mimeType := s.determineMimeType(filename, contentType)

	result, err := s.processStream(file)
	if err != nil {
		return "", err
	}
	defer result.cleanup()

	finalFile, sizeCompressed, alg := s.decideCompression(result)

	if err := s.saveBlob(result.hash, finalFile, result.sizeRaw, sizeCompressed, alg, mimeType); err != nil {
		return "", err
	}

	return s.saveFile(filename, result.hash, oldCumulusID, expiresAt)
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
func (s *FileService) saveBlob(hash string, file *os.File, sizeRaw, sizeCompressed int64, alg, mimeType string) error {
	exists, err := s.MetaStore.BlobExists(hash)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	if exists {
		return nil
	}

	file.Seek(0, 0)
	volID, offset, _, err := s.Store.WriteBlob(file)
	if err != nil {
		return fmt.Errorf("storage error: %w", err)
	}

	category := "unknown"
	subtype := "unknown"
	if parts := strings.Split(mimeType, "/"); len(parts) == 2 {
		category = parts[0]
		subtype = parts[1]
	}

	fileTypeID, err := s.MetaStore.GetOrCreateFileType(mimeType, category, subtype)
	if err != nil {
		return fmt.Errorf("metadata error: %w", err)
	}

	blob := storage.Blob{
		Hash:           hash,
		VolumeID:       volID,
		Offset:         offset,
		SizeRaw:        sizeRaw,
		SizeCompressed: sizeCompressed,
		CompressionAlg: alg,
		FileTypeID:     fileTypeID,
	}
	return s.MetaStore.SaveBlob(blob)
}

// saveFile creates a new file record in the metadata database linked to the blob
func (s *FileService) saveFile(filename, hash string, oldCumulusID *int64, expiresAt *time.Time) (string, error) {
	fileID := uuid.New().String()
	fileMeta := storage.File{
		ID:           fileID,
		Name:         filename,
		BlobHash:     hash,
		OldCumulusID: oldCumulusID,
		ExpiresAt:    expiresAt,
		CreatedAt:    time.Now(),
	}

	if err := s.MetaStore.SaveFile(fileMeta); err != nil {
		return "", fmt.Errorf("metadata error: %w", err)
	}
	return fileID, nil
}
