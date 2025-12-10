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

func NewFileService(store *storage.Store, metaStore *storage.MetadataSQL, compressionMode string, minCompressionRatio float64) *FileService {
	return &FileService{
		Store:               store,
		MetaStore:           metaStore,
		CompressionMode:     compressionMode,
		MinCompressionRatio: minCompressionRatio,
	}
}

func (s *FileService) UploadFile(file io.Reader, filename string, contentType string, oldCumulusID *int64, expiresAt *time.Time) (string, error) {
	// Determine File Type
	mimeType := contentType
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(filename))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
	}

	// Decide compression
	compressionAlg := "none"
	shouldCompress := false
	autoCompress := false

	switch s.CompressionMode {
	case "Gzip":
		shouldCompress = true
		compressionAlg = "gzip"
	case "Zstd":
		shouldCompress = true
		compressionAlg = "zstd"
	case "Auto":
		if !strings.HasPrefix(mimeType, "image/") {
			autoCompress = true
			// We will try Zstd
			compressionAlg = "zstd"
		}
	}

	// Pipeline: Stream -> Hasher + (Compressor)
	hasher, _ := blake2b.New256(nil)

	// Prepare temp files
	// If Auto, we need two temp files: one for original, one for compressed
	// If Forced Compression, one temp file (compressed)
	// If No Compression, one temp file (original)

	var tempFile *os.File
	var tempCompressedFile *os.File
	var err error

	tempFile, err = os.CreateTemp("", "upload-raw-*")
	if err != nil {
		return "", fmt.Errorf("internal error creating temp file: %w", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if autoCompress {
		tempCompressedFile, err = os.CreateTemp("", "upload-comp-*")
		if err != nil {
			return "", fmt.Errorf("internal error creating temp compressed file: %w", err)
		}
		defer os.Remove(tempCompressedFile.Name())
		defer tempCompressedFile.Close()
	}

	// Construct writers
	var writers []io.Writer
	writers = append(writers, hasher)

	var zstdEncoder *zstd.Encoder
	var gzipWriter *gzip.Writer

	if autoCompress {
		// Write raw to tempFile
		writers = append(writers, tempFile)

		// Write compressed to tempCompressedFile
		zstdEncoder, _ = zstd.NewWriter(tempCompressedFile)
		writers = append(writers, zstdEncoder)
	} else if shouldCompress {
		// Write compressed to tempFile
		if compressionAlg == "gzip" {
			gzipWriter = gzip.NewWriter(tempFile)
			writers = append(writers, gzipWriter)
		} else if compressionAlg == "zstd" {
			zstdEncoder, _ = zstd.NewWriter(tempFile)
			writers = append(writers, zstdEncoder)
		}
	} else {
		// Write raw to tempFile
		writers = append(writers, tempFile)
	}

	multiW := io.MultiWriter(writers...)

	sizeRaw, err := io.Copy(multiW, file)
	if err != nil {
		return "", fmt.Errorf("error processing file: %w", err)
	}

	// Close compressors
	if zstdEncoder != nil {
		zstdEncoder.Close()
	}
	if gzipWriter != nil {
		gzipWriter.Close()
	}

	// Sync files
	tempFile.Sync()
	if tempCompressedFile != nil {
		tempCompressedFile.Sync()
	}

	// Decision time for Auto
	finalFile := tempFile
	sizeCompressed := sizeRaw // Default if no compression

	if autoCompress {
		statRaw, _ := tempFile.Stat()
		statComp, _ := tempCompressedFile.Stat()

		sizeRaw = statRaw.Size()
		sizeCompressed = statComp.Size()

		// Calculate savings
		// saved % = (raw - comp) / raw * 100
		savedPercent := (float64(sizeRaw-sizeCompressed) / float64(sizeRaw)) * 100

		if savedPercent >= s.MinCompressionRatio {
			// Use compressed
			finalFile = tempCompressedFile
			compressionAlg = "zstd"
		} else {
			// Use raw
			finalFile = tempFile
			compressionAlg = "none"
			sizeCompressed = sizeRaw
		}
	} else if shouldCompress {
		stat, _ := tempFile.Stat()
		sizeCompressed = stat.Size()
		finalFile = tempFile
	} else {
		// No compression
		finalFile = tempFile
		sizeCompressed = sizeRaw
	}

	hash := hex.EncodeToString(hasher.Sum(nil))

	// Check deduplication
	exists, err := s.MetaStore.BlobExists(hash)
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	if !exists {
		// Write to Volume
		// Rewind temp file
		finalFile.Seek(0, 0)

		volID, offset, _, err := s.Store.WriteBlob(finalFile)
		if err != nil {
			return "", fmt.Errorf("storage error: %w", err)
		}

		// Simple category/subtype parsing
		category := "unknown"
		subtype := "unknown"
		if parts := strings.Split(mimeType, "/"); len(parts) == 2 {
			category = parts[0]
			subtype = parts[1]
		}

		fileTypeID, err := s.MetaStore.GetOrCreateFileType(mimeType, category, subtype)
		if err != nil {
			return "", fmt.Errorf("metadata error: %w", err)
		}

		// Save Blob
		blob := storage.Blob{
			Hash:           hash,
			VolumeID:       volID,
			Offset:         offset,
			SizeRaw:        sizeRaw,
			SizeCompressed: sizeCompressed,
			CompressionAlg: compressionAlg,
			FileTypeID:     fileTypeID,
		}
		if err := s.MetaStore.SaveBlob(blob); err != nil {
			return "", fmt.Errorf("metadata error: %w", err)
		}
	}

	// Save File
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
