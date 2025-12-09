package storage

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type FileMetadata struct {
	ID           string    `json:"id"`
	Hash         string    `json:"hash"`
	OriginalName string    `json:"original_name"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"content_type"`
	CreatedAt    time.Time `json:"created_at"`
}

type MetadataSQL struct {
	db *sql.DB
}

// NewDatabaseSQL initializes SQLite connection
func NewDatabaseSQL(dsn string) (*MetadataSQL, error) {
	// 1. Open DB
	// DSN example: "file:metadata.db?_journal_mode=WAL&_busy_timeout=5000&_sync=NORMAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	// 2. Performance settings
	// Avoid "database is locked" errors
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	// 3. Init Schema
	query := `
	CREATE TABLE IF NOT EXISTS files (
		id TEXT PRIMARY KEY,
		hash TEXT,
		filename TEXT,
		size INTEGER,
		mime_type TEXT,
		created_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash);
	CREATE INDEX IF NOT EXISTS idx_files_mime ON files(mime_type);
	`
	if _, err := db.Exec(query); err != nil {
		return nil, err
	}

	return &MetadataSQL{db: db}, nil
}

func (m *MetadataSQL) Close() error {
	return m.db.Close()
}

// Save inserts a new file record
func (m *MetadataSQL) Save(meta FileMetadata) error {
	query := `
	INSERT INTO files (id, hash, filename, size, mime_type, created_at)
	VALUES (?, ?, ?, ?, ?, ?)
	`
	_, err := m.db.Exec(query, meta.ID, meta.Hash, meta.OriginalName, meta.Size, meta.ContentType, meta.CreatedAt)
	return err
}

// FindByHash retrieves file metadata by hash
func (m *MetadataSQL) FindByHash(hash string) (*FileMetadata, error) {
	query := `
	SELECT id, hash, filename, size, mime_type, created_at
	FROM files WHERE hash = ? LIMIT 1
	`
	row := m.db.QueryRow(query, hash)

	var meta FileMetadata
	err := row.Scan(&meta.ID, &meta.Hash, &meta.OriginalName, &meta.Size, &meta.ContentType, &meta.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &meta, nil
}
