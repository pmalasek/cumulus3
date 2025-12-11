package api

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
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
)

func init() {
	prometheus.MustRegister(uploadOpsTotal)
	prometheus.MustRegister(uploadDuration)
	prometheus.MustRegister(dedupHitsTotal)
	prometheus.MustRegister(storageDeletedBytes)
	prometheus.MustRegister(storageTotalBytes)
}

// UpdateStorageMetrics updates the storage size metrics
func UpdateStorageMetrics(total, deleted int64) {
	storageTotalBytes.Set(float64(total))
	storageDeletedBytes.Set(float64(deleted))
}
