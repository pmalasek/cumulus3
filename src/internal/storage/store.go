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
