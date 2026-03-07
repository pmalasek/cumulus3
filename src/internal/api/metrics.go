package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
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

var uuidPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// normalizePath replaces UUIDs and numeric path segments with placeholder tokens
// so Prometheus does not accumulate high-cardinality per-file label values.
func normalizePath(path string) string {
	path = uuidPattern.ReplaceAllString(path, ":uuid")

	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if isAllDigits(part) {
			parts[i] = ":id"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func isAllDigits(s string) bool {
	if s == "" {
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
