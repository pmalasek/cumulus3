package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/pmalasek/cumulus3/src/internal/utils"
)

// Job tracking for asynchronous operations
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

type Job struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Status      JobStatus  `json:"status"`
	Progress    string     `json:"progress,omitempty"`
	Error       string     `json:"error,omitempty"`
	VolumeID    *int64     `json:"volumeId,omitempty"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type JobManager struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

var globalJobManager = &JobManager{
	jobs: make(map[string]*Job),
}

func (jm *JobManager) CreateJob(jobType string, volumeID *int64) *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	job := &Job{
		ID:        fmt.Sprintf("%s-%d", jobType, time.Now().Unix()),
		Type:      jobType,
		Status:    JobStatusPending,
		VolumeID:  volumeID,
		StartedAt: time.Now(),
	}
	jm.jobs[job.ID] = job
	return job
}

func (jm *JobManager) GetJob(id string) *Job {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.jobs[id]
}

func (jm *JobManager) ListJobs() []*Job {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	jobs := make([]*Job, 0, len(jm.jobs))
	for _, job := range jm.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (jm *JobManager) UpdateJob(id string, status JobStatus, progress string, err error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	job, exists := jm.jobs[id]
	if !exists {
		return
	}

	job.Status = status
	job.Progress = progress
	if err != nil {
		job.Error = err.Error()
	}
	if status == JobStatusCompleted || status == JobStatusFailed {
		now := time.Now()
		job.CompletedAt = &now
	}
}

// System handlers

// HandleSystemStats returns system statistics
// @Summary Get system statistics
// @Description Returns statistics about storage, blobs, files, and deduplication
// @Tags 04 - System
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /system/stats [get]
func (s *Server) HandleSystemStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get storage stats
	totalSize, deletedSize, err := s.FileService.MetaStore.GetStorageStats()
	if err != nil {
		utils.Error("SYSTEM", "Failed to get storage stats: %v", err)
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		return
	}

	// Get blob count and total size
	var blobCount int64
	var blobTotalSize, blobRawSize int64
	err = s.FileService.MetaStore.GetDB().QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(size_compressed), 0), COALESCE(SUM(size_raw), 0)
		FROM blobs
	`).Scan(&blobCount, &blobTotalSize, &blobRawSize)
	if err != nil {
		utils.Error("SYSTEM", "Failed to get blob stats: %v", err)
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		return
	}

	// Get file count
	var fileCount int64
	err = s.FileService.MetaStore.GetDB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&fileCount)
	if err != nil {
		utils.Error("SYSTEM", "Failed to get file stats: %v", err)
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		return
	}

	// Calculate deduplication stats
	deduplicatedCount := fileCount - blobCount
	deduplicationRatio := 0.0
	if fileCount > 0 {
		deduplicationRatio = float64(deduplicatedCount) / float64(fileCount) * 100
	}

	compressionRatio := 0.0
	if blobRawSize > 0 {
		compressionRatio = (1.0 - float64(blobTotalSize)/float64(blobRawSize)) * 100
	}

	stats := map[string]interface{}{
		"blobs": map[string]interface{}{
			"count":            blobCount,
			"totalSize":        blobTotalSize,
			"rawSize":          blobRawSize,
			"compressionRatio": compressionRatio,
		},
		"files": map[string]interface{}{
			"count":              fileCount,
			"deduplicatedCount":  deduplicatedCount,
			"deduplicationRatio": deduplicationRatio,
		},
		"storage": map[string]interface{}{
			"totalSize":          totalSize,
			"deletedSize":        deletedSize,
			"usedSize":           totalSize - deletedSize,
			"fragmentationRatio": float64(deletedSize) / float64(totalSize) * 100,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// HandleSystemVolumes returns list of volumes
// @Summary Get volume list
// @Description Returns list of all volumes with their statistics
// @Tags 04 - System
// @Produce json
// @Success 200 {array} map[string]interface{}
// @Router /system/volumes [get]
func (s *Server) HandleSystemVolumes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	volumes, err := s.FileService.MetaStore.GetVolumesToCompact(0)
	if err != nil {
		utils.Error("SYSTEM", "Failed to get volumes: %v", err)
		http.Error(w, "Failed to get volumes", http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, len(volumes))
	for i, vol := range volumes {
		fragmentation := 0.0
		if vol.SizeTotal > 0 {
			fragmentation = float64(vol.SizeDeleted) / float64(vol.SizeTotal) * 100
		}

		result[i] = map[string]interface{}{
			"id":            vol.ID,
			"totalSize":     vol.SizeTotal,
			"deletedSize":   vol.SizeDeleted,
			"usedSize":      vol.SizeTotal - vol.SizeDeleted,
			"fragmentation": fragmentation,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleSystemCompact triggers volume compaction
// @Summary Compact volume
// @Description Starts asynchronous compaction of a specific volume or all volumes
// @Tags 04 - System
// @Accept json
// @Produce json
// @Param body body map[string]interface{} true "Compact request (volumeId: int or 'all': true)"
// @Success 202 {object} map[string]interface{}
// @Router /system/compact [post]
func (s *Server) HandleSystemCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Check if compacting all volumes
	if all, ok := req["all"].(bool); ok && all {
		job := globalJobManager.CreateJob("compact-all", nil)

		go func() {
			globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Starting compaction of all volumes", nil)

			threshold := 0.0 // Compact all volumes
			if thresholdVal, ok := req["threshold"].(float64); ok {
				threshold = thresholdVal
			}

			volumes, err := s.FileService.MetaStore.GetVolumesToCompact(threshold)
			if err != nil {
				globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
				return
			}

			for i, vol := range volumes {
				progress := fmt.Sprintf("Compacting volume %d (%d/%d)", vol.ID, i+1, len(volumes))
				globalJobManager.UpdateJob(job.ID, JobStatusRunning, progress, nil)

				err := s.FileService.Store.CompactVolume(int64(vol.ID), s.FileService.MetaStore)
				if err != nil {
					utils.Error("COMPACT", "Failed to compact volume %d: %v", vol.ID, err)
					globalJobManager.UpdateJob(job.ID, JobStatusFailed, progress, err)
					return
				}
			}

			globalJobManager.UpdateJob(job.ID, JobStatusCompleted, fmt.Sprintf("Compacted %d volumes", len(volumes)), nil)
		}()

		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jobId":   job.ID,
			"message": "Compaction started",
		})
		return
	}

	// Compact single volume
	volumeID, ok := req["volumeId"].(float64)
	if !ok {
		http.Error(w, "volumeId is required", http.StatusBadRequest)
		return
	}

	volID := int64(volumeID)
	job := globalJobManager.CreateJob("compact", &volID)

	go func() {
		globalJobManager.UpdateJob(job.ID, JobStatusRunning, fmt.Sprintf("Compacting volume %d", volID), nil)

		err := s.FileService.Store.CompactVolume(volID, s.FileService.MetaStore)
		if err != nil {
			globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
			return
		}

		globalJobManager.UpdateJob(job.ID, JobStatusCompleted, "Compaction completed", nil)
	}()

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Compaction started",
	})
}

// HandleSystemJobs returns list of jobs or specific job status
// @Summary Get jobs status
// @Description Returns list of all jobs or specific job details
// @Tags 04 - System
// @Produce json
// @Param id query string false "Job ID"
// @Success 200 {object} map[string]interface{}
// @Router /system/jobs [get]
func (s *Server) HandleSystemJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID := r.URL.Query().Get("id")
	if jobID != "" {
		job := globalJobManager.GetJob(jobID)
		if job == nil {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(job)
		return
	}

	jobs := globalJobManager.ListJobs()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

// HandleSystemIntegrity checks storage integrity
// @Summary Check storage integrity
// @Description Checks integrity of storage (blobs vs files)
// @Tags 04 - System
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /system/integrity [get]
func (s *Server) HandleSystemIntegrity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	job := globalJobManager.CreateJob("integrity-check", nil)

	go func() {
		globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Checking integrity", nil)

		// Check for orphaned blobs (blobs without files)
		var orphanedBlobs int64
		err := s.FileService.MetaStore.GetDB().QueryRow(`
			SELECT COUNT(*) FROM blobs 
			WHERE id NOT IN (SELECT DISTINCT blob_id FROM files)
		`).Scan(&orphanedBlobs)
		if err != nil {
			globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
			return
		}

		// Check for missing blobs (files referencing non-existent blobs)
		var missingBlobs int64
		err = s.FileService.MetaStore.GetDB().QueryRow(`
			SELECT COUNT(*) FROM files 
			WHERE blob_id NOT IN (SELECT id FROM blobs)
		`).Scan(&missingBlobs)
		if err != nil {
			globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
			return
		}

		result := map[string]interface{}{
			"orphanedBlobs": orphanedBlobs,
			"missingBlobs":  missingBlobs,
			"status":        "ok",
		}

		if orphanedBlobs > 0 || missingBlobs > 0 {
			result["status"] = "warning"
		}

		// Store result in job progress as JSON
		progressJSON, _ := json.Marshal(result)
		globalJobManager.UpdateJob(job.ID, JobStatusCompleted, string(progressJSON), nil)
	}()

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Integrity check started",
	})
}
