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
)

func init() {
	prometheus.MustRegister(uploadOpsTotal)
	prometheus.MustRegister(uploadDuration)
	prometheus.MustRegister(dedupHitsTotal)
}
