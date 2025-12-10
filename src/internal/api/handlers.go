package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/pmalasek/cumulus3/docs"
	"github.com/pmalasek/cumulus3/src/internal/service"
	"github.com/pmalasek/cumulus3/src/internal/utils"
	httpSwagger "github.com/swaggo/http-swagger"
)

type Server struct {
	FileService   *service.FileService
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
// @Param tags formData string false "Tags like array of string or coma separated strings"
// @Param old_cumulus_id formData int false "Legacy ID"
// @Param validity formData string false "Validity period (e.g. '1 day', '2 months')"
// @Success 201 {object} map[string]string "File uploaded successfully"
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
		exp, err := utils.ParseValidity(val)
		if err != nil {
			http.Error(w, "Invalid validity format: "+err.Error(), http.StatusBadRequest)
			return
		}
		expiresAt = &exp
	}

	// Process tags
	var tags []string
	if values, ok := r.Form["tags"]; ok {
		for _, v := range values {
			parts := strings.Split(v, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					tags = append(tags, trimmed)
				}
			}
		}
	}
	tagsStr := strings.Join(tags, ",")

	cleanFilename := filepath.Base(header.Filename)
	fmt.Printf("DATA : %s %v %v %s\n", cleanFilename, oldCumulusID, expiresAt, tagsStr)
	// Call FileService
	fileID, err := s.FileService.UploadFile(file, cleanFilename, header.Header.Get("Content-Type"), oldCumulusID, expiresAt, tagsStr)
	if err != nil {
		// We should probably log the error and return 500
		// For now, just return 500
		http.Error(w, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"fileID": fileID})
}

func (s *Server) HandleDownload(w http.ResponseWriter, r *http.Request) {
	// Placeholder
	w.WriteHeader(http.StatusNotImplemented)
}

// {
//   "fileID": "f73c3c10-f516-4a6b-9f5c-daae1b42e710"
// }
