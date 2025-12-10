package storage

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type File struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	BlobID       int64      `json:"blob_id"`
	OldCumulusID *int64     `json:"old_cumulus_id,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	Tags         string     `json:"tags,omitempty"`
}

type Blob struct {
	ID             int64  `json:"id"`
	Hash           string `json:"hash"`
	VolumeID       int64  `json:"volume_id"`
	Offset         int64  `json:"offset"`
	SizeRaw        int64  `json:"size_raw"`
	SizeCompressed int64  `json:"size_compressed"`
	CompressionAlg string `json:"compression_alg"`
	FileTypeID     int64  `json:"file_type_id"`
}

type FileType struct {
	ID       int64  `json:"id"`
	MimeType string `json:"mime_type"`
	Category string `json:"category"`
	Subtype  string `json:"subtype"`
}

type MetadataSQL struct {
	db *sql.DB
}

// NewMetadataSQL initializes SQLite connection
func NewMetadataSQL(dsn string) (*MetadataSQL, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	if err := initSchema(db); err != nil {
		return nil, err
	}

	return &MetadataSQL{db: db}, nil
}

func initSchema(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS file_types (
			id INTEGER PRIMARY KEY,
			mime_type TEXT UNIQUE,
			category TEXT,
			subtype TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS blobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hash TEXT UNIQUE,
			volume_id INTEGER,
			offset INTEGER,
			size_raw INTEGER,
			size_compressed INTEGER,
			compression_alg TEXT,
			file_type_id INTEGER,
			FOREIGN KEY(file_type_id) REFERENCES file_types(id)
		);`,
		`CREATE TABLE IF NOT EXISTS files (
			id TEXT PRIMARY KEY,
			name TEXT,
			blob_id INTEGER,
			old_cumulus_id INTEGER,
			expires_at DATETIME,
			created_at DATETIME,
			tags TEXT,
			FOREIGN KEY(blob_id) REFERENCES blobs(id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_files_expires_at ON files(expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_files_old_cumulus_id ON files(old_cumulus_id);`,
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}

	// Migration: Add tags column if not exists
	_, _ = db.Exec("ALTER TABLE files ADD COLUMN tags TEXT")

	return nil
}

func (m *MetadataSQL) Close() error {
	return m.db.Close()
}

func (m *MetadataSQL) SaveFile(file File) error {
	query := `
	INSERT INTO files (id, name, blob_id, old_cumulus_id, expires_at, created_at, tags)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := m.db.Exec(query, file.ID, file.Name, file.BlobID, file.OldCumulusID, file.ExpiresAt, file.CreatedAt, file.Tags)
	return err
}

func (m *MetadataSQL) CleanupExpiredFiles() (int64, error) {
	query := `DELETE FROM files WHERE expires_at < CURRENT_TIMESTAMP`
	res, err := m.db.Exec(query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (m *MetadataSQL) GetBlobIDByHash(hash string) (int64, bool, error) {
	var id int64
	query := `SELECT id FROM blobs WHERE hash = ?`
	err := m.db.QueryRow(query, hash).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (m *MetadataSQL) CreateBlob(hash string) (int64, error) {
	query := `INSERT INTO blobs (hash) VALUES (?)`
	res, err := m.db.Exec(query, hash)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (m *MetadataSQL) UpdateBlobLocation(id int64, volumeID, offset, sizeRaw, sizeCompressed int64, compressionAlg string, fileTypeID int64) error {
	query := `
	UPDATE blobs 
	SET volume_id = ?, offset = ?, size_raw = ?, size_compressed = ?, compression_alg = ?, file_type_id = ?
	WHERE id = ?
	`
	_, err := m.db.Exec(query, volumeID, offset, sizeRaw, sizeCompressed, compressionAlg, fileTypeID, id)
	return err
}

func (m *MetadataSQL) GetOrCreateFileType(mimeType, category, subtype string) (int64, error) {
	var id int64
	err := m.db.QueryRow("SELECT id FROM file_types WHERE mime_type = ?", mimeType).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	res, err := m.db.Exec("INSERT INTO file_types (mime_type, category, subtype) VALUES (?, ?, ?)", mimeType, category, subtype)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (m *MetadataSQL) FileExistsByOldID(oldID int64) (bool, error) {
	var count int
	err := m.db.QueryRow("SELECT count(*) FROM files WHERE old_cumulus_id = ?", oldID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
