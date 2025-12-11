package storage

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
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
	volumeLocks     sync.Map
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

func (s *Store) getVolumeLock(volumeID int64) *sync.Mutex {
	v, _ := s.volumeLocks.LoadOrStore(volumeID, &sync.Mutex{})
	return v.(*sync.Mutex)
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

// WriteBlob zapíše data do volume souboru s optimalizovanou hlavičkou (BlobID)
func (s *Store) WriteBlob(blobID int64, data []byte, compressionAlg uint8) (volumeID int64, offset int64, err error) {
	// Get current volume ID under short lock
	s.mu.Lock()
	currentVol := s.CurrentVolumeID
	s.mu.Unlock()

	// Lock only this specific volume to allow parallel writes to different volumes
	volLock := s.getVolumeLock(currentVol)
	volLock.Lock()
	defer volLock.Unlock()

	// Check if volume changed during wait for lock (rotation happened)
	s.mu.Lock()
	if s.CurrentVolumeID != currentVol {
		s.mu.Unlock()
		// Volume rotated, retry with new volume
		return s.WriteBlob(blobID, data, compressionAlg)
	}
	s.mu.Unlock()

	dataSize := int64(len(data))
	totalEntrySize := int64(HeaderSize) + dataSize + int64(FooterSize)

	filename := fmt.Sprintf("volume_%08d.dat", currentVol)
	fullPath := filepath.Join(s.BaseDir, filename)

	// If new format doesn't exist, check if legacy exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		filenameLegacy := fmt.Sprintf("volume_%d.dat", currentVol)
		fullPathLegacy := filepath.Join(s.BaseDir, filenameLegacy)
		if _, err := os.Stat(fullPathLegacy); err == nil {
			filename = filenameLegacy
			fullPath = fullPathLegacy
		}
	}

	// Check if we need to rotate
	if stat, err := os.Stat(fullPath); err == nil {
		if stat.Size()+totalEntrySize > s.MaxDataFileSize {
			s.mu.Lock()
			s.CurrentVolumeID++
			s.mu.Unlock()
			// Retry with new volume
			return s.WriteBlob(blobID, data, compressionAlg)
		}
	}

	volumeID = currentVol

	f, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	offset = stat.Size()

	// Výpočet CRC (pro integritu)
	crc := crc32.ChecksumIEEE(data)

	// 1. HLAVIČKA
	// Magic(4) + Ver(1) + Comp(1) + Size(8) + BlobID(8)
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(header[0:4], uint32(MagicBytes))
	header[4] = Version
	header[5] = compressionAlg
	binary.BigEndian.PutUint64(header[6:14], uint64(dataSize))
	binary.BigEndian.PutUint64(header[14:22], uint64(blobID))

	if _, err := f.Write(header); err != nil {
		return 0, 0, err
	}

	// 2. DATA
	if _, err := f.Write(data); err != nil {
		return 0, 0, err
	}

	// 3. PATIČKA
	footer := make([]byte, FooterSize)
	binary.BigEndian.PutUint32(footer[0:4], crc)

	if _, err := f.Write(footer); err != nil {
		return 0, 0, err
	}

	// 4. Zápis do META souboru (Index)
	// Formát: BlobID(8) + Offset(8) + Size(8) + Comp(1) + CRC(4) = 29 bytes
	metaFilename := strings.TrimSuffix(filename, ".dat") + ".meta"
	metaPath := filepath.Join(s.BaseDir, metaFilename)
	mf, err := os.OpenFile(metaPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Pokud selže zápis meta, je to kritické? Pro konzistenci raději ano.
		return 0, 0, err
	}
	defer mf.Close()

	metaRecord := make([]byte, 29)
	binary.BigEndian.PutUint64(metaRecord[0:8], uint64(blobID))
	binary.BigEndian.PutUint64(metaRecord[8:16], uint64(offset))     // Offset začátku blobu (hlavičky) v .dat
	binary.BigEndian.PutUint64(metaRecord[16:24], uint64(len(data))) // Velikost komprimovaných dat
	metaRecord[24] = compressionAlg
	binary.BigEndian.PutUint32(metaRecord[25:29], crc)

	if _, err := mf.Write(metaRecord); err != nil {
		return 0, 0, err
	}

	return volumeID, offset, nil
}

// ReadBlob přečte data z volume souboru
func (s *Store) ReadBlob(volumeID int64, offset int64, size int64) ([]byte, error) {
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
