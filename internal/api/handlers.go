package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/pmalasek/cumulus3/docs"
	httpSwagger "github.com/swaggo/http-swagger"

	// Ujisti se, že cesta odpovídá tvému go.mod
	"github.com/pmalasek/cumulus3/internal/storage"
)

type Server struct {
	Store         *storage.Store
	MetaStore     *storage.MetadataSQL
	MaxUploadSize int64
}

// -----------------------------

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
// @Description Uploads a file to the storage and saves its metadata to BadgerDB
// @Tags files
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "File to upload"
// @Param tags formData string false "Comma-separated tags"
// @Success 201 {string} string "File uploaded successfully"
// @Failure 400 {string} string "Bad Request"
// @Failure 500 {string} string "Internal Server Error"
// @Router /api/v1/files [post]
func (s *Server) HandleUpload(w http.ResponseWriter, r *http.Request) {
	// 1. Parse Multipart Form (max size from config)
	if err := r.ParseMultipartForm(s.MaxUploadSize); err != nil {
		http.Error(w, "Could not parse multipart form", http.StatusBadRequest)
		return
	}

	// 2. Get file from form
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 3. Prepare metadata
	filename := header.Filename
	contentType := header.Header.Get("Content-Type")
	size := header.Size

	// Parse tags (assuming comma separated)
	var tags []string
	tagsStr := r.FormValue("tags")
	if tagsStr != "" {
		for t := range strings.SplitSeq(tagsStr, ",") {
			tags = append(tags, strings.TrimSpace(t))
		}
	}
	fmt.Println("Tags", tags)

	// Calculate Hash while writing to disk
	hasher := sha256.New()
	tee := io.TeeReader(file, hasher)

	// 4. Save file to disk
	if err := s.Store.WriteFile(filename, tee); err != nil {
		http.Error(w, "Error saving file to disk", http.StatusInternalServerError)
		return
	}

	hash := hex.EncodeToString(hasher.Sum(nil))
	id := uuid.New().String()

	// 5. Save metadata to DB
	meta := storage.FileMetadata{
		ID:           id,
		Hash:         hash,
		OriginalName: filename,
		Size:         size,
		ContentType:  contentType,
		CreatedAt:    time.Now(),
	}

	if err := s.MetaStore.Save(meta); err != nil {
		http.Error(w, "Error saving metadata", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "File %s uploaded successfully", filename)
}

// HandleDownload downloads a file
// @Summary Download a file
// @Description Downloads a file by its filename
// @Tags files
// @Produce octet-stream
// @Param filename path string true "Filename"
// @Success 200 {file} file "File content"
// @Failure 404 {string} string "Not Found"
// @Router /api/v1/files/{filename} [get]
func (s *Server) HandleDownload(w http.ResponseWriter, r *http.Request) {
	// ... tvůj kód ...
	fmt.Println("response", w.Header())
	fmt.Println("response", r.Header)

}
