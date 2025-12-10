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
	s.mu.Lock()
	defer s.mu.Unlock()

	dataSize := int64(len(data))
	totalEntrySize := int64(HeaderSize) + dataSize + int64(FooterSize)

	filename := fmt.Sprintf("volume_%08d.dat", s.CurrentVolumeID)
	fullPath := filepath.Join(s.BaseDir, filename)

	// If new format doesn't exist, check if legacy exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		filenameLegacy := fmt.Sprintf("volume_%d.dat", s.CurrentVolumeID)
		fullPathLegacy := filepath.Join(s.BaseDir, filenameLegacy)
		if _, err := os.Stat(fullPathLegacy); err == nil {
			filename = filenameLegacy
			fullPath = fullPathLegacy
		}
	}

	// Check if we need to rotate
	if stat, err := os.Stat(fullPath); err == nil {
		if stat.Size()+totalEntrySize > s.MaxDataFileSize {
			s.CurrentVolumeID++
			// New volume always uses new format
			filename = fmt.Sprintf("volume_%08d.dat", s.CurrentVolumeID)
			fullPath = filepath.Join(s.BaseDir, filename)
		}
	}

	volumeID = s.CurrentVolumeID

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
	}

	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, err
	}

	// 1. Hlavička
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, err
	}

	magic := binary.BigEndian.Uint32(header[0:4])
	// ver := header[4]
	// comp := header[5]
	storedSize := int64(binary.BigEndian.Uint64(header[6:14]))
	// blobID := int64(binary.BigEndian.Uint64(header[14:22]))

	if magic != uint32(MagicBytes) {
		return nil, os.ErrInvalid // Bad magic
	}
	if storedSize != size {
		return nil, os.ErrInvalid // Size mismatch
	}

	// 2. Data
	data := make([]byte, storedSize)
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}

	// 3. Patička
	footer := make([]byte, FooterSize)
	if _, err := io.ReadFull(f, footer); err != nil {
		return nil, err
	}

	expectedCrc := binary.BigEndian.Uint32(footer[0:4])
	actualCrc := crc32.ChecksumIEEE(data)

	if expectedCrc != actualCrc {
		return nil, os.ErrInvalid // CRC mismatch
	}

	return data, nil
}
