package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
// @Description Checks integrity of storage (blobs vs files). Use ?deep=true for physical verification
// @Tags 04 - System
// @Produce json
// @Param deep query boolean false "Perform deep integrity check (verifies physical files)"
// @Success 200 {object} map[string]interface{}
// @Router /system/integrity [get]
func (s *Server) HandleSystemIntegrity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	deepCheck := r.URL.Query().Get("deep") == "true"
	jobType := "integrity-check"
	if deepCheck {
		jobType = "integrity-check-deep"
	}

	job := globalJobManager.CreateJob(jobType, nil)

	go func() {
		if deepCheck {
			s.performDeepIntegrityCheck(job)
		} else {
			s.performQuickIntegrityCheck(job)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Integrity check started",
	})
}

func (s *Server) performQuickIntegrityCheck(job *Job) {
	globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Checking database integrity", nil)

	// Check for orphaned blobs (blobs without files)
	var orphanedBlobs int64
	err := s.FileService.MetaStore.GetDB().QueryRow(`
			SELECT COUNT(*) FROM blobs b
			LEFT JOIN files f ON b.id = f.blob_id
			WHERE f.blob_id IS NULL
		`).Scan(&orphanedBlobs)
	if err != nil {
		globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
		return
	}

	// Check for missing blobs (files referencing non-existent blobs)
	var missingBlobs int64
	err = s.FileService.MetaStore.GetDB().QueryRow(`
			SELECT COUNT(*) FROM files f
			LEFT JOIN blobs b ON f.blob_id = b.id
			WHERE b.id IS NULL
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
}

func (s *Server) performDeepIntegrityCheck(job *Job) {
	globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Starting deep integrity check", nil)

	result := map[string]interface{}{
		"orphanedBlobs":     int64(0),
		"missingBlobs":      int64(0),
		"missingVolumes":    []int{},
		"unreadableBlobs":   int64(0),
		"totalBlobsChecked": int64(0),
		"status":            "ok",
	}

	// First run quick checks
	globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Checking database consistency", nil)

	var orphanedBlobs, missingBlobs int64
	err := s.FileService.MetaStore.GetDB().QueryRow(`
		SELECT COUNT(*) FROM blobs b
		LEFT JOIN files f ON b.id = f.blob_id
		WHERE f.blob_id IS NULL
	`).Scan(&orphanedBlobs)
	if err != nil {
		globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
		return
	}
	result["orphanedBlobs"] = orphanedBlobs

	err = s.FileService.MetaStore.GetDB().QueryRow(`
		SELECT COUNT(*) FROM files f
		LEFT JOIN blobs b ON f.blob_id = b.id
		WHERE b.id IS NULL
	`).Scan(&missingBlobs)
	if err != nil {
		globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
		return
	}
	result["missingBlobs"] = missingBlobs

	// Check physical volumes exist
	globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Checking volume files on disk", nil)

	rows, err := s.FileService.MetaStore.GetDB().Query(`
		SELECT DISTINCT volume_id FROM blobs ORDER BY volume_id
	`)
	if err != nil {
		globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
		return
	}
	defer rows.Close()

	missingVolumes := []int{}
	for rows.Next() {
		var volumeID int
		if err := rows.Scan(&volumeID); err != nil {
			continue
		}

		// Check if volume file exists
		volumePath := fmt.Sprintf("%s/volume_%08d.dat", s.FileService.Store.BaseDir, volumeID)
		if _, err := os.Stat(volumePath); os.IsNotExist(err) {
			// Try legacy format
			volumePath = fmt.Sprintf("%s/volume_%d.dat", s.FileService.Store.BaseDir, volumeID)
			if _, err := os.Stat(volumePath); os.IsNotExist(err) {
				missingVolumes = append(missingVolumes, volumeID)
			}
		}
	}
	result["missingVolumes"] = missingVolumes

	// Check blob readability (sample check - read first 100 bytes of each blob)
	globalJobManager.UpdateJob(job.ID, JobStatusRunning, "Verifying blob readability", nil)

	blobRows, err := s.FileService.MetaStore.GetDB().Query(`
		SELECT id, volume_id, offset, size_compressed FROM blobs ORDER BY volume_id, offset LIMIT 1000
	`)
	if err != nil {
		globalJobManager.UpdateJob(job.ID, JobStatusFailed, "", err)
		return
	}
	defer blobRows.Close()

	unreadableBlobs := int64(0)
	totalChecked := int64(0)
	currentVolume := -1
	var volumeFile *os.File

	for blobRows.Next() {
		var blobID, volumeID int64
		var offset, sizeCompressed int64
		if err := blobRows.Scan(&blobID, &volumeID, &offset, &sizeCompressed); err != nil {
			continue
		}

		totalChecked++
		if totalChecked%100 == 0 {
			globalJobManager.UpdateJob(job.ID, JobStatusRunning,
				fmt.Sprintf("Checked %d/%d blobs", totalChecked, 1000), nil)
		}

		// Open volume file if needed
		if currentVolume != int(volumeID) {
			if volumeFile != nil {
				volumeFile.Close()
			}
			volumePath := fmt.Sprintf("%s/volume_%08d.dat", s.FileService.Store.BaseDir, volumeID)
			volumeFile, err = os.Open(volumePath)
			if err != nil {
				// Try legacy format
				volumePath = fmt.Sprintf("%s/volume_%d.dat", s.FileService.Store.BaseDir, volumeID)
				volumeFile, err = os.Open(volumePath)
				if err != nil {
					unreadableBlobs++
					continue
				}
			}
			currentVolume = int(volumeID)
		}

		// Try to read first 100 bytes of blob (header)
		testBuffer := make([]byte, 100)
		_, err := volumeFile.ReadAt(testBuffer, offset)
		if err != nil && err != io.EOF {
			unreadableBlobs++
		}
	}

	if volumeFile != nil {
		volumeFile.Close()
	}

	result["unreadableBlobs"] = unreadableBlobs
	result["totalBlobsChecked"] = totalChecked

	// Determine overall status
	if missingBlobs > 0 || len(missingVolumes) > 0 || unreadableBlobs > 0 {
		result["status"] = "error"
	} else if orphanedBlobs > 0 {
		result["status"] = "warning"
	}

	progressJSON, _ := json.Marshal(result)
	globalJobManager.UpdateJob(job.ID, JobStatusCompleted, string(progressJSON), nil)
}
