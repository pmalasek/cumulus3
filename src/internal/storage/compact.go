package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (s *Store) CompactVolume(volumeID int64, meta *MetadataSQL) error {
	// Determine if it is current volume and acquire locks in correct order (s.mu then volLock)
	// This prevents deadlock with WriteBlob which acquires s.mu then volLock
	s.mu.Lock()
	isCurrent := (volumeID == s.CurrentVolumeID)
	if !isCurrent {
		s.mu.Unlock()
	} else {
		defer s.mu.Unlock()
	}

	// Lock the volume exclusively (write lock) - blocks all reads and writes
	lock := s.getVolumeLock(volumeID)
	lock.Lock()
	defer lock.Unlock()

	// 1. Create temporary file
	filename := fmt.Sprintf("volume_%08d.dat", volumeID)
	compactFilename := fmt.Sprintf("volume_%08d.dat.compact", volumeID)

	// Check if legacy name exists if new doesn't
	fullPath := filepath.Join(s.BaseDir, filename)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		// Try legacy
		legacyName := fmt.Sprintf("volume_%d.dat", volumeID)
		if _, err := os.Stat(filepath.Join(s.BaseDir, legacyName)); err == nil {
			filename = legacyName
			fullPath = filepath.Join(s.BaseDir, filename)
			compactFilename = fmt.Sprintf("volume_%d.dat.compact", volumeID)
		} else {
			return fmt.Errorf("volume file not found: %s", filename)
		}
	}

	compactPath := filepath.Join(s.BaseDir, compactFilename)
	compactFile, err := os.Create(compactPath)
	if err != nil {
		return err
	}
	defer compactFile.Close()

	// Open original file
	originalFile, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer originalFile.Close()

	// 2. Iterate blobs
	rows, err := meta.db.Query("SELECT id, hash, offset, size_compressed FROM blobs WHERE volume_id = ? ORDER BY offset ASC", volumeID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type BlobUpdate struct {
		ID        int64
		NewOffset int64
	}
	var updates []BlobUpdate
	var currentOffset int64 = 0

	// Reusable buffer to reduce allocations
	maxBlobSize := int64(1 << 20) // 1 MB initial size
	buffer := make([]byte, maxBlobSize)

	for rows.Next() {
		var id, offset, sizeCompressed int64
		var hash string
		if err := rows.Scan(&id, &hash, &offset, &sizeCompressed); err != nil {
			return err
		}

		// Read blob data
		// Calculate total size including header/footer
		blobTotalSize := int64(HeaderSize) + sizeCompressed + int64(FooterSize)

		// Grow buffer if needed
		if blobTotalSize > int64(len(buffer)) {
			buffer = make([]byte, blobTotalSize)
		}
		usedBuffer := buffer[:blobTotalSize]

		if _, err := originalFile.ReadAt(usedBuffer, offset); err != nil {
			return fmt.Errorf("failed to read blob %d: %w", id, err)
		}

		// Write to compact file
		n, err := compactFile.Write(usedBuffer)
		if err != nil {
			return err
		}
		if int64(n) != blobTotalSize {
			return io.ErrShortWrite
		}

		updates = append(updates, BlobUpdate{ID: id, NewOffset: currentOffset})
		currentOffset += blobTotalSize
	}

	// 3. Transaction update
	tx, err := meta.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	updateStmt, err := tx.Prepare("UPDATE blobs SET offset = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer updateStmt.Close()

	for _, u := range updates {
		if _, err := updateStmt.Exec(u.NewOffset, u.ID); err != nil {
			return err
		}
	}

	// Update volumes table
	// set size_deleted = 0, size_total = new_size
	if _, err := tx.Exec("UPDATE volumes SET size_total = ?, size_deleted = 0 WHERE id = ?", currentOffset, volumeID); err != nil {
		return err
	}

	// 4. Close files before swap
	originalFile.Close()
	compactFile.Close()

	// 5. Swap files BEFORE committing transaction
	// This ensures if rename fails, transaction is rolled back
	if err := os.Rename(compactPath, fullPath); err != nil {
		return err
	}

	// 6. Commit transaction after successful file swap
	if err := tx.Commit(); err != nil {
		// Critical: file is renamed but DB update failed
		// Try to restore old file (best effort)
		os.Rename(fullPath, compactPath)
		return fmt.Errorf("failed to commit transaction after file swap: %w", err)
	}

	// 7. Truncate file to actual size to free disk space
	// This removes the "holes" left by deleted data
	if err := os.Truncate(fullPath, currentOffset); err != nil {
		// Non-critical error, just log it
		// File is still valid, just larger than needed
		return fmt.Errorf("warning: failed to truncate volume file: %w", err)
	}

	// 8. Recalculate current volume if this was a current or newer volume
	// This allows the system to switch back to this volume if it now has space
	if isCurrent {
		// We already hold s.mu.Lock, use no-lock version
		s.recalculateCurrentVolumeNoLock()
	} else if volumeID >= s.CurrentVolumeID {
		// Need to acquire lock for recalculation
		s.RecalculateCurrentVolume()
	}

	// 9. Regenerate .meta file with updated offsets
	if err := s.regenerateMetaFile(volumeID, meta); err != nil {
		// Non-critical error, just log warning
		// The .meta file is used for fast recovery, but database is the source of truth
		return fmt.Errorf("warning: failed to regenerate .meta file: %w", err)
	}

	return nil
}
