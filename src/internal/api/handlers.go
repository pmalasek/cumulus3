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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	httpSwagger "github.com/swaggo/http-swagger"
)

type Server struct {
	FileService   *service.FileService
	MaxUploadSize int64
}

// Routes vytvoří router a zaregistruje cesty
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.HandleHealth)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/v2/files/upload", s.HandleUpload)
	mux.HandleFunc("/v2/files/", s.HandleDownload)
	mux.HandleFunc("/v2/files/info/", s.HandleFileInfo)
	mux.HandleFunc("/base/files/id/", s.HandleDownloadByOldID)
	mux.HandleFunc("/base/files/info/", s.HandleFileInfoByOldID)
	mux.HandleFunc("/base/files/delete/", s.HandleDelete)
	mux.HandleFunc("/docs/", httpSwagger.WrapHandler)
	return mux
}

// HandleHealth returns service health status
// @Summary Health check
// @Description Returns OK if service is healthy
// @Tags system
// @Produce json
// @Success 200 {object} map[string]string
// @Router /health [get]
func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "cumulus3",
	})
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
// @Router /v2/files/upload [post]
func (s *Server) HandleUpload(w http.ResponseWriter, r *http.Request) {
	timer := prometheus.NewTimer(uploadDuration)
	defer timer.ObserveDuration()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.MaxUploadSize)
	if err := r.ParseMultipartForm(s.MaxUploadSize); err != nil {
		utils.Info("UPLOAD", " Failed to parse form from %s: %v", r.RemoteAddr, err)
		http.Error(w, "File too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		utils.Info("UPLOAD", " Error retrieving file from %s: %v", r.RemoteAddr, err)
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
	utils.Info("UPLOAD", " Starting upload: filename=%s, content_type=%s, size=%d, old_id=%v, expires=%v, tags=%s, remote=%s",
		cleanFilename, header.Header.Get("Content-Type"), header.Size, oldCumulusID, expiresAt, tagsStr, r.RemoteAddr)

	// Determine file type for metrics
	contentType := header.Header.Get("Content-Type")
	fileTypeLabel := "unknown"
	if parts := strings.Split(contentType, "/"); len(parts) > 0 {
		fileTypeLabel = parts[0]
	}

	// Call FileService
	fileID, isDedup, err := s.FileService.UploadFileWithDedup(file, cleanFilename, contentType, oldCumulusID, expiresAt, tagsStr)
	if err != nil {
		uploadOpsTotal.WithLabelValues("error", fileTypeLabel).Inc()
		utils.Info("UPLOAD", " ERROR: filename=%s, remote=%s, error=%v", cleanFilename, r.RemoteAddr, err)
		http.Error(w, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	uploadOpsTotal.WithLabelValues("success", fileTypeLabel).Inc()
	if isDedup {
		dedupHitsTotal.Inc()
	}
	utils.Info("UPLOAD", " SUCCESS: filename=%s, file_id=%s, dedup=%v, remote=%s", cleanFilename, fileID, isDedup, r.RemoteAddr)

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
// @Router /v2/files/{id} [get]
func (s *Server) HandleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract ID from URL
	// URL is /v2/files/{id}
	id := strings.TrimPrefix(r.URL.Path, "/v2/files/")
	if id == "" || id == "/" {
		utils.Info("DOWNLOAD", " Missing file ID from %s", r.RemoteAddr)
		http.Error(w, "Missing file ID", http.StatusBadRequest)
		return
	}

	utils.Info("DOWNLOAD", " Requesting file_id=%s, remote=%s", id, r.RemoteAddr)
	data, filename, mimeType, err := s.FileService.DownloadFile(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			utils.Info("DOWNLOAD", " File not found: file_id=%s, remote=%s", id, r.RemoteAddr)
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		utils.Info("DOWNLOAD", " ERROR: file_id=%s, remote=%s, error=%v", id, r.RemoteAddr, err)
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
	utils.Info("DOWNLOAD", " SUCCESS: file_id=%s, filename=%s, size=%d, mime=%s, remote=%s", id, filename, len(data), mimeType, r.RemoteAddr)
}

// HandleFileInfo retrieves file information
// @Summary Get file info
// @Description Get detailed information about a file
// @Tags files
// @Produce json
// @Param id path string true "File ID"
// @Param extended query boolean false "Include base64 content"
// @Success 200 {object} service.FileInfo
// @Failure 400 {string} string "Bad Request"
// @Failure 404 {string} string "File not found"
// @Failure 500 {string} string "Internal Server Error"
// @Router /v2/files/info/{id} [get]
func (s *Server) HandleFileInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fileID := strings.TrimPrefix(r.URL.Path, "/v2/files/info/")
	if fileID == "" || fileID == "/" {
		utils.Info("FILE_INFO", " Missing file ID from %s", r.RemoteAddr)
		http.Error(w, "Missing file ID", http.StatusBadRequest)
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
			utils.Info("FILE_INFO", " File not found: file_id=%s, remote=%s", fileID, r.RemoteAddr)
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		utils.Info("FILE_INFO", " ERROR: file_id=%s, remote=%s, error=%v", fileID, r.RemoteAddr, err)
		http.Error(w, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	utils.Info("FILE_INFO", " SUCCESS: file_id=%s, extended=%v, remote=%s", fileID, extended, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// HandleDownloadByOldID downloads a file by its old Cumulus ID
// @Summary Download a file by old ID
// @Description Downloads a file by its old Cumulus ID
// @Tags files
// @Produce octet-stream
// @Param id path int true "Old Cumulus ID"
// @Success 200 {file} file "File content"
// @Failure 404 {string} string "File not found"
// @Failure 500 {string} string "Internal Server Error"
// @Router /base/files/id/{id} [get]
func (s *Server) HandleDownloadByOldID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/base/files/id/")
	if idStr == "" || idStr == "/" {
		http.Error(w, "Missing file ID", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		utils.Info("DOWNLOAD_OLD_ID", " Invalid ID format: id=%s, remote=%s, error=%v", idStr, r.RemoteAddr, err)
		http.Error(w, "Invalid file ID", http.StatusBadRequest)
		return
	}

	utils.Info("DOWNLOAD_OLD_ID", " Requesting old_id=%d, remote=%s", id, r.RemoteAddr)
	data, filename, mimeType, err := s.FileService.DownloadFileByOldID(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			utils.Info("DOWNLOAD_OLD_ID", " File not found: old_id=%d, remote=%s", id, r.RemoteAddr)
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		utils.Info("DOWNLOAD_OLD_ID", " ERROR: old_id=%d, remote=%s, error=%v", id, r.RemoteAddr, err)
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
	utils.Info("DOWNLOAD_OLD_ID", " SUCCESS: old_id=%d, filename=%s, size=%d, mime=%s, remote=%s", id, filename, len(data), mimeType, r.RemoteAddr)
}

// HandleFileInfoByOldID retrieves file information by old Cumulus ID
// @Summary Get file info by old ID
// @Description Get detailed information about a file by its old Cumulus ID
// @Tags files
// @Produce json
// @Param id path int true "Old Cumulus ID"
// @Param extended query boolean false "Include base64 content"
// @Success 200 {object} service.FileInfo
// @Failure 400 {string} string "Bad Request"
// @Failure 404 {string} string "File not found"
// @Failure 500 {string} string "Internal Server Error"
// @Router /base/files/info/{id} [get]
func (s *Server) HandleFileInfoByOldID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/base/files/info/")
	if idStr == "" || idStr == "/" {
		http.Error(w, "Missing file ID", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid file ID", http.StatusBadRequest)
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

	info, err := s.FileService.GetFileInfoByOldID(id, extended)
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

// HandleDelete deletes a file
// @Summary Delete a file
// @Description Deletes a file by its ID
// @Tags files
// @Param id path string true "File ID"
// @Success 200 {string} string "File deleted successfully"
// @Failure 400 {string} string "Bad Request"
// @Failure 500 {string} string "Internal Server Error"
// @Router /base/files/delete/{id} [delete]
func (s *Server) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/base/files/delete/")
	if id == "" {
		utils.Info("DELETE", " Missing file ID from %s", r.RemoteAddr)
		http.Error(w, "File ID is required", http.StatusBadRequest)
		return
	}

	utils.Info("DELETE", " Deleting file_id=%s, remote=%s", id, r.RemoteAddr)
	err := s.FileService.DeleteFile(id)
	if err != nil {
		utils.Info("DELETE", " ERROR: file_id=%s, remote=%s, error=%v", id, r.RemoteAddr, err)
		http.Error(w, "Error deleting file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	utils.Info("DELETE", " SUCCESS: file_id=%s, remote=%s", id, r.RemoteAddr)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("File deleted successfully"))
}
