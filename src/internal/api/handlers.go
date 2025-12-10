package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	mux.HandleFunc("/api/v1/file_info", s.HandleFileInfo)
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

// HandleDownload downloads a file
// @Summary Download a file
// @Description Downloads a file by its ID
// @Tags files
// @Produce octet-stream
// @Param id path string true "File ID"
// @Success 200 {file} file "File content"
// @Failure 404 {string} string "File not found"
// @Failure 500 {string} string "Internal Server Error"
// @Router /api/v1/files/{id} [get]
func (s *Server) HandleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract ID from URL
	// URL is /api/v1/files/{id}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")
	if id == "" || id == "/" {
		http.Error(w, "Missing file ID", http.StatusBadRequest)
		return
	}

	data, filename, mimeType, err := s.FileService.DownloadFile(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", mimeType)
	encodedFilename := url.PathEscape(filename)

	// Determine disposition based on mime type
	disposition := "attachment"
	if strings.HasPrefix(mimeType, "image/") ||
		strings.HasPrefix(mimeType, "video/") ||
		strings.HasPrefix(mimeType, "audio/") ||
		mimeType == "application/pdf" ||
		mimeType == "text/plain" {
		disposition = "inline"
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", disposition, filename, encodedFilename))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// HandleFileInfo retrieves file information
// @Summary Get file info
// @Description Get detailed information about a file
// @Tags files
// @Produce json
// @Param file_ID query string true "File ID"
// @Param extended query boolean false "Include base64 content"
// @Success 200 {object} service.FileInfo
// @Failure 400 {string} string "Bad Request"
// @Failure 404 {string} string "File not found"
// @Failure 500 {string} string "Internal Server Error"
// @Router /api/v1/file_info [get]
func (s *Server) HandleFileInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fileID := r.URL.Query().Get("file_ID")
	if fileID == "" {
		http.Error(w, "Missing file_ID parameter", http.StatusBadRequest)
		return
	}

	extendedStr := r.URL.Query().Get("extended")
	extended := false
	if extendedStr != "" {
		var err error
		extended, err = strconv.ParseBool(extendedStr)
		if err != nil {
			http.Error(w, "Invalid extended parameter", http.StatusBadRequest)
			return
		}
	}

	info, err := s.FileService.GetFileInfo(fileID, extended)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// {
//   "fileID": "f73c3c10-f516-4a6b-9f5c-daae1b42e710",
//   "fileID": "91995bdd-9466-4ace-8183-ef02b3c0cd14",  // PDF
//   "fileID": "efc284fa-75d9-411b-8099-56fecfdebf46"   // PDF
//   "fileID": "8113be17-8fb2-47b6-b2c5-5a26e92b62ab"
// }
