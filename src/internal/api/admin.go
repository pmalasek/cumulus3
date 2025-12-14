package api

import (
	"embed"
	"net/http"
	"os"
)

//go:embed static/*
var staticFiles embed.FS

// HandleAdmin serves the admin UI
func (s *Server) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	content, err := staticFiles.ReadFile("static/admin.html")
	if err != nil {
		http.Error(w, "Failed to load admin page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}

// HandleAdminScript serves the admin JavaScript
func (s *Server) HandleAdminScript(w http.ResponseWriter, r *http.Request) {
	content, err := staticFiles.ReadFile("static/admin.js")
	if err != nil {
		http.Error(w, "Failed to load script", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(content)
}

// Admin authentication middleware
func AdminAuthMiddleware(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Admin Area"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func GetAdminCredentials() (string, string) {
	username := os.Getenv("ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}
	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		password = "admin"
	}
	return username, password
}
