package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// HTTP metriky
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
		[]string{"method", "path"},
	)

	httpRequestsInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Current number of HTTP requests being processed.",
		},
	)

	uploadOpsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "upload_ops_total",
			Help: "Total number of file upload operations.",
		},
		[]string{"status", "file_type"},
	)

	uploadDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "upload_duration_seconds",
			Help:    "Duration of file upload requests.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
	)

	dedupHitsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "storage_dedup_hits_total",
			Help: "Total number of storage deduplication hits.",
		},
	)

	storageDeletedBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "storage_deleted_bytes_total",
			Help: "Total bytes marked as deleted in storage volumes.",
		},
	)

	storageTotalBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "storage_bytes_total",
			Help: "Total bytes in storage volumes.",
		},
	)

	// BLOB I/O metriky
	blobBytesWritten = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "blob_bytes_written_total",
			Help: "Total bytes written to BLOB storage.",
		},
	)

	blobBytesRead = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "blob_bytes_read_total",
			Help: "Total bytes read from BLOB storage.",
		},
	)
)

func init() {
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(httpRequestDuration)
	prometheus.MustRegister(httpRequestsInFlight)
	prometheus.MustRegister(uploadOpsTotal)
	prometheus.MustRegister(uploadDuration)
	prometheus.MustRegister(dedupHitsTotal)
	prometheus.MustRegister(storageDeletedBytes)
	prometheus.MustRegister(storageTotalBytes)
	prometheus.MustRegister(blobBytesWritten)
	prometheus.MustRegister(blobBytesRead)
}

// UpdateStorageMetrics updates the storage size metrics
func UpdateStorageMetrics(total, deleted int64) {
	storageTotalBytes.Set(float64(total))
	storageDeletedBytes.Set(float64(deleted))
}

// RecordBlobBytesWritten records bytes written to BLOB storage
func RecordBlobBytesWritten(bytes int64) {
	blobBytesWritten.Add(float64(bytes))
}

// RecordBlobBytesRead records bytes read from BLOB storage
func RecordBlobBytesRead(bytes int) {
	blobBytesRead.Add(float64(bytes))
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// normalizePath converts paths with IDs to normalized patterns
func normalizePath(path string) string {
	// Replace UUID patterns (8-4-4-4-12 hex format)
	if len(path) > 36 {
		// Match UUID pattern in path
		for i := 0; i < len(path)-36; i++ {
			segment := path[i : i+36]
			// Simple UUID check: contains dashes at positions 8, 13, 18, 23
			if len(segment) == 36 && segment[8] == '-' && segment[13] == '-' &&
				segment[18] == '-' && segment[23] == '-' {
				path = path[:i] + ":uuid" + path[i+36:]
				break
			}
		}
	}

	// Replace numeric IDs in common patterns
	// /base/files/old/12345 -> /base/files/old/:id
	// /v2/files/old/12345 -> /v2/files/old/:id
	parts := []string{}
	for _, part := range splitPath(path) {
		if isNumeric(part) {
			parts = append(parts, ":id")
		} else {
			parts = append(parts, part)
		}
	}
	return joinPath(parts)
}

func splitPath(path string) []string {
	parts := []string{}
	current := ""
	for _, c := range path {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func joinPath(parts []string) string {
	if len(parts) == 0 {
		return "/"
	}
	result := ""
	for _, part := range parts {
		result += "/" + part
	}
	return result
}

func isNumeric(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// MetricsMiddleware measures HTTP request metrics
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip metrics endpoint to avoid recursion
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		httpRequestsInFlight.Inc()
		defer httpRequestsInFlight.Dec()

		// Wrap response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		normalizedPath := normalizePath(r.URL.Path)

		// Record metrics with normalized path
		httpRequestsTotal.WithLabelValues(r.Method, normalizedPath, strconv.Itoa(rw.statusCode)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, normalizedPath).Observe(duration)
	})
}
