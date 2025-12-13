package storage

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	MagicBytes = 0x43554D55
	Version    = 1
	// Header: Magic(4) + Ver(1) + Comp(1) + Size(8) + BlobID(8)
	HeaderSize = 4 + 1 + 1 + 8 + 8
	FooterSize = 4
)

// Store reprezentuje naše úložiště
type Store struct {
	BaseDir         string
	MaxDataFileSize int64
	mu              sync.Mutex
	CurrentVolumeID int64
	volumeLocks     sync.Map // map[int64]*sync.RWMutex
}

// NewStore vytvoří novou instanci a připraví složku
func NewStore(dir string, maxDataFileSize int64) *Store {
	_ = os.MkdirAll(dir, 0755)

	// Find the latest volume ID
	var currentVolumeID int64 = 1
	for {
		exists := false
		// Check new format
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("volume_%08d.dat", currentVolumeID))); err == nil {
			exists = true
		}
		// Check legacy format
		if !exists {
			if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("volume_%d.dat", currentVolumeID))); err == nil {
				exists = true
			}
		}

		if !exists {
			if currentVolumeID > 1 {
				currentVolumeID--
			}
			break
		}
		currentVolumeID++
	}

	return &Store{
		BaseDir:         dir,
		MaxDataFileSize: maxDataFileSize,
		CurrentVolumeID: currentVolumeID,
	}
}

func (s *Store) getVolumeLock(volumeID int64) *sync.RWMutex {
	v, _ := s.volumeLocks.LoadOrStore(volumeID, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// RecalculateCurrentVolume finds the first volume that has space available
// Useful after compaction to switch back to a volume that now has space
func (s *Store) RecalculateCurrentVolume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recalculateCurrentVolumeNoLock()
}

// recalculateCurrentVolumeNoLock is internal version without locking
// Call this when you already hold s.mu.Lock()
func (s *Store) recalculateCurrentVolumeNoLock() {
	// Start from volume 1 and find the first one that has space
	for volumeID := int64(1); volumeID <= s.CurrentVolumeID; volumeID++ {
		filename := fmt.Sprintf("volume_%08d.dat", volumeID)
		fullPath := filepath.Join(s.BaseDir, filename)

		// Check for legacy format
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			filenameLegacy := fmt.Sprintf("volume_%d.dat", volumeID)
			fullPathLegacy := filepath.Join(s.BaseDir, filenameLegacy)
			if _, err := os.Stat(fullPathLegacy); err == nil {
				fullPath = fullPathLegacy
			} else {
				// Volume doesn't exist, skip
				continue
			}
		}

		// Check if volume has space
		if stat, err := os.Stat(fullPath); err == nil {
			if stat.Size() < s.MaxDataFileSize {
				// Found a volume with space, switch to it
				s.CurrentVolumeID = volumeID
				return
			}
		}
	}

	// All volumes are full, keep current (or create next one)
}

// findVolumeWithSpaceNoLock finds first volume (from 1 to current) that has enough space
// Uses database metadata if available, otherwise falls back to file system
// skipLocked: if true, skips volumes that are currently locked (e.g., being compacted)
// Returns volume ID to use. Call this when you already hold s.mu.Lock()
func (s *Store) findVolumeWithSpaceNoLock(requiredSize int64, meta *MetadataSQL, skipLocked bool) int64 {
	if meta != nil {
		// Use database values (source of truth)
		volumes, err := meta.GetVolumesToCompact(0) // Get all volumes
		if err == nil {
			// Build a map for quick lookup
			volMap := make(map[int64]int64) // volumeID -> size_total
			for _, vol := range volumes {
				volMap[int64(vol.ID)] = vol.SizeTotal
			}

			// Check each volume from 1 to current
			for volumeID := int64(1); volumeID <= s.CurrentVolumeID; volumeID++ {
				// Check if volume exists in DB
				sizeTotal, exists := volMap[volumeID]
				if !exists {
					// Volume not in DB yet, assume empty (size = 0)
					sizeTotal = 0
				}

				// Check if volume has enough space based on DB values
				if sizeTotal+requiredSize <= s.MaxDataFileSize {
					// Found a volume with enough space
					return volumeID
				}
			}
		}
	}

	// Fallback to file system check (when no metadata available or volume not in DB yet)
	// Try existing volumes first (from 1 to current)
	for volumeID := int64(1); volumeID <= s.CurrentVolumeID; volumeID++ {
		// Skip locked volumes if requested
		if skipLocked {
			lock := s.getVolumeLock(volumeID)
			if !lock.TryLock() {
				continue
			}
			lock.Unlock()
		}

		filename := fmt.Sprintf("volume_%08d.dat", volumeID)
		fullPath := filepath.Join(s.BaseDir, filename)

		// Check for legacy format
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			filenameLegacy := fmt.Sprintf("volume_%d.dat", volumeID)
			fullPathLegacy := filepath.Join(s.BaseDir, filenameLegacy)
			if _, err := os.Stat(fullPathLegacy); err == nil {
				fullPath = fullPathLegacy
			} else {
				// Volume doesn't exist yet, skip
				continue
			}
		}

		// Check if volume has enough space based on file size
		if stat, err := os.Stat(fullPath); err == nil {
			if stat.Size()+requiredSize <= s.MaxDataFileSize {
				// Found a volume with enough space
				return volumeID
			}
		}
	}

	// All existing volumes are full, create/use next one
	s.CurrentVolumeID++
	return s.CurrentVolumeID
}

// WriteFile uloží data na disk (legacy)
func (s *Store) WriteFile(filename string, data io.Reader) error {
	fullPath := filepath.Join(s.BaseDir, filename)
	dst, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, data)
	return err
}

// ReadFile vrátí čtečku pro soubor
func (s *Store) ReadFile(filename string) (io.ReadCloser, error) {
	fullPath := filepath.Join(s.BaseDir, filename)
	return os.Open(fullPath)
}

// WriteBlob zapíše data do volume souboru
// Returns: volumeID, offset, totalBytesWritten (including header and footer), error
func (s *Store) WriteBlob(blobID int64, data []byte, compressionAlg uint8) (volumeID int64, offset int64, totalSize int64, err error) {
	return s.WriteBlobWithMetadata(blobID, data, compressionAlg, nil)
}

// WriteBlobWithMetadata zapíše data do volume souboru s využitím DB metadat pro nalezení volume s místem
// Returns: volumeID, offset, totalBytesWritten (including header and footer), error
func (s *Store) WriteBlobWithMetadata(blobID int64, data []byte, compressionAlg uint8, meta *MetadataSQL) (volumeID int64, offset int64, totalSize int64, err error) {
	dataSize := int64(len(data))
	totalEntrySize := int64(HeaderSize) + dataSize + int64(FooterSize)

	// Find a volume with enough space (tries from volume 1 up to current)
	// Skip locked volumes (e.g., being compacted) to avoid blocking
	s.mu.Lock()
	targetVol := s.findVolumeWithSpaceNoLock(totalEntrySize, meta, true)
	s.mu.Unlock()

	// Try to write to selected volume, with retry if it's full
	// This handles race condition where multiple goroutines pass the initial check
	var f *os.File
	var filename, fullPath string
	triedVolumes := make(map[int64]bool) // Track which volumes we already tried
	maxRetries := 100                    // Prevent infinite loop

	for len(triedVolumes) < maxRetries {
		// Check if we already tried this volume
		if triedVolumes[targetVol] {
			// Already tried this volume, move to next
			s.mu.Lock()
			if targetVol >= s.CurrentVolumeID {
				s.CurrentVolumeID++
				targetVol = s.CurrentVolumeID
			} else {
				targetVol++
			}
			s.mu.Unlock()
			continue
		}
		triedVolumes[targetVol] = true

		// Lock this specific volume to allow parallel writes to different volumes
		volLock := s.getVolumeLock(targetVol)
		volLock.Lock()

		// Double-check if volume still has space after acquiring lock
		// Another goroutine might have filled it while we were waiting
		if meta != nil {
			var currentSize int64
			err := meta.db.QueryRow("SELECT COALESCE(size_total, 0) FROM volumes WHERE id = ?", targetVol).Scan(&currentSize)
			if err != nil && err != sql.ErrNoRows {
				// Database error (not just missing row)
				volLock.Unlock()
				return 0, 0, 0, fmt.Errorf("failed to check volume size: %w", err)
			}
			// If err == sql.ErrNoRows, currentSize stays 0 (new volume)

			if currentSize+totalEntrySize > s.MaxDataFileSize {
				// Volume is full after all, unlock and try next one
				volLock.Unlock()

				// Log if we've tried many volumes already
				if len(triedVolumes) > 10 {
					log.Printf("WARNING: Volume %d is full (size=%d, required=%d, max=%d), tried %d volumes so far",
						targetVol, currentSize, totalEntrySize, s.MaxDataFileSize, len(triedVolumes))
				}

				// Try next volume
				s.mu.Lock()
				if targetVol >= s.CurrentVolumeID {
					s.CurrentVolumeID++
					targetVol = s.CurrentVolumeID
				} else {
					targetVol++
				}
				s.mu.Unlock()
				continue
			}
		}

		// Volume has space, proceed with write
		volumeID = targetVol
		filename = fmt.Sprintf("volume_%08d.dat", targetVol)
		fullPath = filepath.Join(s.BaseDir, filename)

		// If new format doesn't exist, check if legacy exists
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			filenameLegacy := fmt.Sprintf("volume_%d.dat", targetVol)
			fullPathLegacy := filepath.Join(s.BaseDir, filenameLegacy)
			if _, err := os.Stat(fullPathLegacy); err == nil {
				filename = filenameLegacy
				fullPath = fullPathLegacy
			}
		}

		f, err = os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			volLock.Unlock()
			return 0, 0, 0, err
		}
		defer f.Close()
		defer volLock.Unlock()

		stat, err := f.Stat()
		if err != nil {
			return 0, 0, 0, err
		}
		offset = stat.Size()

		// Write blob to the end of file
		if err := s.writeBlobData(f, blobID, data, compressionAlg); err != nil {
			return 0, 0, 0, err
		}

		// Write to META file (Index)
		metaFilename := strings.TrimSuffix(filename, ".dat") + ".meta"
		metaPath := filepath.Join(s.BaseDir, metaFilename)
		if err := s.writeMetaRecord(metaPath, blobID, offset, data, compressionAlg); err != nil {
			return 0, 0, 0, err
		}

		// Update volumes table BEFORE releasing lock to ensure atomic check + update
		// This prevents race condition where multiple goroutines read old size_total
		totalBytesWritten := int64(HeaderSize) + int64(len(data)) + int64(FooterSize)
		if meta != nil {
			_, err := meta.db.Exec(`
				INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, ?, 0)
				ON CONFLICT(id) DO UPDATE SET size_total = size_total + ?
			`, volumeID, totalBytesWritten, totalBytesWritten)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("failed to update volume size: %w", err)
			}
		}

		// Success, break out of retry loop
		break
	}

	// Check if we exited loop without success (reached max retries)
	if volumeID == 0 {
		return 0, 0, 0, fmt.Errorf("failed to write blob after trying %d volumes: all volumes are full or locked", len(triedVolumes))
	}

	// Return actual bytes written (header + data + footer)
	totalBytesWritten := int64(HeaderSize) + int64(len(data)) + int64(FooterSize)
	return volumeID, offset, totalBytesWritten, nil
}

// ReadBlob přečte data z volume souboru
func (s *Store) ReadBlob(volumeID int64, offset int64, size int64) ([]byte, error) {
	// Use RLock to allow parallel reads, but block during compaction (which uses Lock)
	lock := s.getVolumeLock(volumeID)
	lock.RLock()
	defer lock.RUnlock()

	filename := fmt.Sprintf("volume_%08d.dat", volumeID)
	fullPath := filepath.Join(s.BaseDir, filename)

	f, err := os.Open(fullPath)
	if os.IsNotExist(err) {
		// Fallback for legacy filenames
		filenameLegacy := fmt.Sprintf("volume_%d.dat", volumeID)
		fullPathLegacy := filepath.Join(s.BaseDir, filenameLegacy)
		f, err = os.Open(fullPathLegacy)
		if err != nil {
			return nil, fmt.Errorf("volume file not found (tried %s and %s): %w", filename, filenameLegacy, err)
		}
		fullPath = fullPathLegacy
	} else if err != nil {
		return nil, fmt.Errorf("cannot open volume file %s: %w", fullPath, err)
	}
	defer f.Close()

	// Get file size for validation
	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot stat volume file: %w", err)
	}
	fileSize := stat.Size()

	// Validate offset
	if offset < 0 || offset >= fileSize {
		return nil, fmt.Errorf("invalid offset %d (file size: %d, volume: %s)", offset, fileSize, fullPath)
	}

	// Validate that we can read header + data + footer
	requiredSize := offset + HeaderSize + size + FooterSize
	if requiredSize > fileSize {
		return nil, fmt.Errorf("blob extends beyond file end (offset: %d, size: %d, required: %d, file size: %d, volume: %s)",
			offset, size, requiredSize, fileSize, fullPath)
	}

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, fmt.Errorf("seek to offset %d failed: %w", offset, err)
	}

	// 1. Hlavička
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, fmt.Errorf("cannot read header at offset %d: %w", offset, err)
	}

	magic := binary.BigEndian.Uint32(header[0:4])
	ver := header[4]
	comp := header[5]
	storedSize := int64(binary.BigEndian.Uint64(header[6:14]))
	blobID := int64(binary.BigEndian.Uint64(header[14:22]))

	if magic != uint32(MagicBytes) {
		return nil, fmt.Errorf("bad magic bytes at offset %d: got 0x%X, expected 0x%X", offset, magic, MagicBytes)
	}
	if storedSize != size {
		return nil, fmt.Errorf("size mismatch at offset %d: header says %d, metadata says %d (blobID: %d, ver: %d, comp: %d)",
			offset, storedSize, size, blobID, ver, comp)
	}

	// 2. Data
	data := make([]byte, storedSize)
	if n, err := io.ReadFull(f, data); err != nil {
		return nil, fmt.Errorf("cannot read data at offset %d (expected %d bytes, got %d): %w", offset+HeaderSize, storedSize, n, err)
	}

	// 3. Patička
	footer := make([]byte, FooterSize)
	if _, err := io.ReadFull(f, footer); err != nil {
		return nil, fmt.Errorf("cannot read footer at offset %d: %w", offset+HeaderSize+storedSize, err)
	}

	expectedCrc := binary.BigEndian.Uint32(footer[0:4])
	actualCrc := crc32.ChecksumIEEE(data)

	if expectedCrc != actualCrc {
		return nil, fmt.Errorf("CRC mismatch at offset %d: expected 0x%X, got 0x%X (blobID: %d)", offset, expectedCrc, actualCrc, blobID)
	}

	return data, nil
}

// writeBlobData writes the blob header, data, and footer to a file
func (s *Store) writeBlobData(f *os.File, blobID int64, data []byte, compressionAlg uint8) error {
	dataSize := int64(len(data))
	crc := crc32.ChecksumIEEE(data)

	// 1. HLAVIČKA
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(header[0:4], uint32(MagicBytes))
	header[4] = Version
	header[5] = compressionAlg
	binary.BigEndian.PutUint64(header[6:14], uint64(dataSize))
	binary.BigEndian.PutUint64(header[14:22], uint64(blobID))

	if _, err := f.Write(header); err != nil {
		return err
	}

	// 2. DATA
	if _, err := f.Write(data); err != nil {
		return err
	}

	// 3. PATIČKA
	footer := make([]byte, FooterSize)
	binary.BigEndian.PutUint32(footer[0:4], crc)

	if _, err := f.Write(footer); err != nil {
		return err
	}

	return nil
}

// writeMetaRecord writes a metadata record to the .meta file
func (s *Store) writeMetaRecord(metaPath string, blobID int64, offset int64, data []byte, compressionAlg uint8) error {
	crc := crc32.ChecksumIEEE(data)

	mf, err := os.OpenFile(metaPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer mf.Close()

	// Formát: BlobID(8) + Offset(8) + Size(8) + Comp(1) + CRC(4) = 29 bytes
	metaRecord := make([]byte, 29)
	binary.BigEndian.PutUint64(metaRecord[0:8], uint64(blobID))
	binary.BigEndian.PutUint64(metaRecord[8:16], uint64(offset))
	binary.BigEndian.PutUint64(metaRecord[16:24], uint64(len(data)))
	metaRecord[24] = compressionAlg
	binary.BigEndian.PutUint32(metaRecord[25:29], crc)

	_, err = mf.Write(metaRecord)
	return err
}

// regenerateMetaFile regenerates the .meta file after compaction with updated offsets
func (s *Store) regenerateMetaFile(volumeID int64, meta *MetadataSQL) error {
	// Get all blobs for this volume from database (with correct offsets after compaction)
	rows, err := meta.db.Query(`
		SELECT b.id, b.offset, b.size_compressed, b.compression_alg 
		FROM blobs b 
		WHERE b.volume_id = ? 
		ORDER BY b.offset ASC
	`, volumeID)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Determine filename
	filename := fmt.Sprintf("volume_%08d.dat", volumeID)
	fullPath := filepath.Join(s.BaseDir, filename)

	// Check for legacy format
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		legacyName := fmt.Sprintf("volume_%d.dat", volumeID)
		if _, err := os.Stat(filepath.Join(s.BaseDir, legacyName)); err == nil {
			filename = legacyName
		}
	}

	metaFilename := strings.TrimSuffix(filename, ".dat") + ".meta"
	metaPath := filepath.Join(s.BaseDir, metaFilename)

	// Create new .meta file (overwrite old one)
	mf, err := os.Create(metaPath)
	if err != nil {
		return err
	}
	defer mf.Close()

	// Write all blob records with updated offsets
	for rows.Next() {
		var blobID, offset, sizeCompressed int64
		var compressionAlg string
		if err := rows.Scan(&blobID, &offset, &sizeCompressed, &compressionAlg); err != nil {
			return err
		}

		// Convert compression alg string to byte code
		var compAlgCode uint8 = 0
		switch compressionAlg {
		case "gzip":
			compAlgCode = 1
		case "zstd":
			compAlgCode = 2
		}

		// We need to calculate CRC from the actual data
		// For now, write 0 as CRC (recovery tool doesn't strictly need it)
		// TODO: Could read data and calculate proper CRC if needed
		crc := uint32(0)

		// Formát: BlobID(8) + Offset(8) + Size(8) + Comp(1) + CRC(4) = 29 bytes
		metaRecord := make([]byte, 29)
		binary.BigEndian.PutUint64(metaRecord[0:8], uint64(blobID))
		binary.BigEndian.PutUint64(metaRecord[8:16], uint64(offset))
		binary.BigEndian.PutUint64(metaRecord[16:24], uint64(sizeCompressed))
		metaRecord[24] = compAlgCode
		binary.BigEndian.PutUint32(metaRecord[25:29], crc)

		if _, err := mf.Write(metaRecord); err != nil {
			return err
		}
	}

	return rows.Err()
}
