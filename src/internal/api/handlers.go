package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/pmalasek/cumulus3/docs"
	"github.com/pmalasek/cumulus3/src/internal/service"
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

	// Call FileService
	fileID, err := s.FileService.UploadFile(file, header.Filename, header.Header.Get("Content-Type"), oldCumulusID, expiresAt)
	if err != nil {
		// We should probably log the error and return 500
		// For now, just return 500
		http.Error(w, "Internal Server Error: "+err.Error(), http.StatusInternalServerError)
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
