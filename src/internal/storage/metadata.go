package storage

import (
	"database/sql"
)

type VolumeInfo struct {
	ID          int
	SizeTotal   int64
	SizeDeleted int64
}

func (m *MetadataSQL) IncrementDeletedSize(volumeID int64, bytes int64) error {
	query := `
INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, 0, ?)
ON CONFLICT(id) DO UPDATE SET size_deleted = size_deleted + ?
`
	_, err := m.db.Exec(query, volumeID, bytes, bytes)
	return err
}

func (m *MetadataSQL) GetVolumesToCompact(threshold float64) ([]VolumeInfo, error) {
	var query string
	var rows *sql.Rows
	var err error

	if threshold <= 0 {
		// threshold=0 means get all volumes
		query = `
SELECT id, size_total, size_deleted 
FROM volumes 
WHERE size_total > 0
ORDER BY id`
		rows, err = m.db.Query(query)
	} else {
		// Convert threshold from percentage (5.0 = 5%) to ratio (0.05)
		thresholdRatio := threshold / 100.0

		query = `
SELECT id, size_total, size_deleted 
FROM volumes 
WHERE size_total > 0 AND CAST(size_deleted AS FLOAT) / CAST(size_total AS FLOAT) > ?
ORDER BY id`
		rows, err = m.db.Query(query, thresholdRatio)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var volumes []VolumeInfo
	for rows.Next() {
		var v VolumeInfo
		if err := rows.Scan(&v.ID, &v.SizeTotal, &v.SizeDeleted); err != nil {
			return nil, err
		}
		volumes = append(volumes, v)
	}
	return volumes, nil
}

func (m *MetadataSQL) DeleteFile(fileID string) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Get blob ID before deleting
	var blobID int64
	err = tx.QueryRow("SELECT blob_id FROM files WHERE id = ?", fileID).Scan(&blobID)
	if err == sql.ErrNoRows {
		return nil // File doesn't exist, nothing to do
	}
	if err != nil {
		return err
	}

	// Delete file
	if _, err = tx.Exec("DELETE FROM files WHERE id = ?", fileID); err != nil {
		return err
	}

	// Check ref count
	var count int
	err = tx.QueryRow("SELECT count(*) FROM files WHERE blob_id = ?", blobID).Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		// Blob is no longer referenced.
		// Get blob info to know volume and size
		var volumeID, sizeCompressed int64
		err = tx.QueryRow("SELECT volume_id, size_compressed FROM blobs WHERE id = ?", blobID).Scan(&volumeID, &sizeCompressed)
		if err != nil {
			return err
		}

		// Calculate total size (Header + Compressed + Footer)
		totalSize := int64(HeaderSize) + sizeCompressed + int64(FooterSize)

		// Update volumes table
		volQuery := `
INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, 0, ?)
ON CONFLICT(id) DO UPDATE SET size_deleted = size_deleted + ?
`
		if _, err = tx.Exec(volQuery, volumeID, totalSize, totalSize); err != nil {
			return err
		}

		// Delete the blob record so it's not copied during compaction
		if _, err = tx.Exec("DELETE FROM blobs WHERE id = ?", blobID); err != nil {
			return err
		}
	}

	err = tx.Commit()
	return err
}

func (m *MetadataSQL) GetStorageStats() (int64, int64, error) {
	var total, deleted sql.NullInt64
	query := `SELECT SUM(size_total), SUM(size_deleted) FROM volumes`
	err := m.db.QueryRow(query).Scan(&total, &deleted)
	if err != nil {
		return 0, 0, err
	}
	return total.Int64, deleted.Int64, nil
}

// CleanupExpiredTemporaryFiles finds and deletes expired temporary files
// that are safe to delete (their blob is not referenced by any other valid file)
// Returns the number of successfully deleted files and any error encountered
func (m *MetadataSQL) CleanupExpiredTemporaryFiles() (int, int, int, error) {
	// Get list of expired file IDs that are safe to delete
	fileIDs, totalExpired, err := m.GetExpiredTemporaryFiles()
	if err != nil {
		return 0, totalExpired, 0, err
	}

	safeToDel := len(fileIDs)
	if safeToDel == 0 {
		return 0, totalExpired, 0, nil
	}

	deletedCount := 0
	failedCount := 0
	failedIDs := []string{}

	for _, fileID := range fileIDs {
		if err := m.DeleteFile(fileID); err != nil {
			// Log error but continue with other files
			failedCount++
			failedIDs = append(failedIDs, fileID)
			continue
		}
		deletedCount++
	}

	return deletedCount, totalExpired, safeToDel, nil
}
