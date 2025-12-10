package storage

import (
	"io"
	"os"
	"path/filepath"
)

// Store reprezentuje naše úložiště
type Store struct {
	BaseDir         string
	MaxDataFileSize int64
}

// NewStore vytvoří novou instanci a připraví složku
func NewStore(dir string, maxDataFileSize int64) *Store {
	_ = os.MkdirAll(dir, 0755)
	return &Store{
		BaseDir:         dir,
		MaxDataFileSize: maxDataFileSize,
	}
}

// WriteFile uloží data na disk (zatím jednoduše)
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

// WriteBlob appends data to a volume file and returns location info
func (s *Store) WriteBlob(data io.Reader) (volumeID int64, offset int64, bytesWritten int64, err error) {
	// For simplicity, we use a single volume file "volume_1.dat"
	volumeID = 1
	filename := "volume_1.dat"
	fullPath := filepath.Join(s.BaseDir, filename)

	f, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return 0, 0, 0, err
	}
	offset = stat.Size()

	bytesWritten, err = io.Copy(f, data)
	if err != nil {
		return 0, 0, 0, err
	}

	return volumeID, offset, bytesWritten, nil
}
