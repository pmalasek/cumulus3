package api

import (
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/pmalasek/cumulus3/docs"
	"github.com/pmalasek/cumulus3/src/internal/storage"
	httpSwagger "github.com/swaggo/http-swagger"
	"golang.org/x/crypto/blake2b"
)

type Server struct {
	Store         *storage.Store
	MetaStore     *storage.MetadataSQL
	MaxUploadSize int64
}

// Routes vytvoří router a zaregistruje cesty
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files", s.HandleUpload)
	mux.HandleFunc("/api/v1/files/", s.HandleDownload)
	mux.HandleFunc("/docs/", httpSwagger.WrapHandler)
	return mux
}

// HandleUpload uploads a file and saves metadata
// @Summary Upload a file
// @Description Uploads a file to the storage
// @Tags files
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "File to upload"
// @Param old_cumulus_id formData int false "Legacy ID"
// @Param validity formData string false "Validity period (e.g. '1 day', '2 months')"
// @Success 201 {string} string "File uploaded successfully"
// @Failure 400 {string} string "Bad Request"
// @Failure 413 {string} string "File too large"
// @Failure 500 {string} string "Internal Server Error"
// @Router /api/v1/files [post]
func (s *Server) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.MaxUploadSize)
	if err := r.ParseMultipartForm(s.MaxUploadSize); err != nil {
		http.Error(w, "File too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Process optional fields
	var oldCumulusID *int64
	if val := r.FormValue("old_cumulus_id"); val != "" {
		id, err := strconv.ParseInt(val, 10, 64)
		if err == nil {
			oldCumulusID = &id
		}
	}

	var expiresAt *time.Time
	if val := r.FormValue("validity"); val != "" {
		exp, err := parseValidity(val)
		if err != nil {
			http.Error(w, "Invalid validity format: "+err.Error(), http.StatusBadRequest)
			return
		}
		expiresAt = &exp
	}

	// Pipeline: Stream -> Hasher + GZIP
	hasher, _ := blake2b.New256(nil)

	// Use a temporary file for the compressed output
	tempFile, err := os.CreateTemp("", "upload-*")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tempFile.Name()) // Clean up
	defer tempFile.Close()

	gzipW := gzip.NewWriter(tempFile)
	multiW := io.MultiWriter(hasher, gzipW)

	sizeRaw, err := io.Copy(multiW, file)
	if err != nil {
		http.Error(w, "Error processing file", http.StatusInternalServerError)
		return
	}
	gzipW.Close() // Flush gzip
	tempFile.Sync()

	hash := hex.EncodeToString(hasher.Sum(nil))

	// Check deduplication
	exists, err := s.MetaStore.BlobExists(hash)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if !exists {
		// Write to Volume
		// Rewind temp file
		tempFile.Seek(0, 0)

		// Get compressed size
		stat, _ := tempFile.Stat()
		sizeCompressed := stat.Size()

		volID, offset, _, err := s.Store.WriteBlob(tempFile)
		if err != nil {
			http.Error(w, "Storage error", http.StatusInternalServerError)
			return
		}

		// Determine File Type
		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = mime.TypeByExtension(filepath.Ext(header.Filename))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
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
			http.Error(w, "Metadata error", http.StatusInternalServerError)
			return
		}

		// Save Blob
		blob := storage.Blob{
			Hash:           hash,
			VolumeID:       volID,
			Offset:         offset,
			SizeRaw:        sizeRaw,
			SizeCompressed: sizeCompressed,
			CompressionAlg: "gzip",
			FileTypeID:     fileTypeID,
		}
		if err := s.MetaStore.SaveBlob(blob); err != nil {
			http.Error(w, "Metadata error", http.StatusInternalServerError)
			return
		}
	}

	// Save File
	fileID := uuid.New().String()
	fileMeta := storage.File{
		ID:           fileID,
		Name:         header.Filename,
		BlobHash:     hash,
		OldCumulusID: oldCumulusID,
		ExpiresAt:    expiresAt,
		CreatedAt:    time.Now(),
	}

	if err := s.MetaStore.SaveFile(fileMeta); err != nil {
		http.Error(w, "Metadata error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "File uploaded successfully. ID: %s", fileID)
}

func (s *Server) HandleDownload(w http.ResponseWriter, r *http.Request) {
	// Placeholder
	w.WriteHeader(http.StatusNotImplemented)
}

func parseValidity(val string) (time.Time, error) {
	parts := strings.Fields(val)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid format")
	}
	amount, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid amount")
	}
	unit := strings.ToLower(parts[1])

	var d time.Duration
	switch {
	case strings.HasPrefix(unit, "day"):
		d = time.Duration(amount) * 24 * time.Hour
	case strings.HasPrefix(unit, "month"):
		d = time.Duration(amount) * 30 * 24 * time.Hour // Approx
	default:
		return time.Time{}, fmt.Errorf("unknown unit")
	}

	if d < 24*time.Hour {
		return time.Time{}, fmt.Errorf("minimum validity is 1 day")
	}
	if d > 365*24*time.Hour {
		return time.Time{}, fmt.Errorf("maximum validity is 1 year")
	}

	return time.Now().Add(d), nil
}
