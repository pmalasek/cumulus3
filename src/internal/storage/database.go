package storage

import (
	"database/sql"
	"encoding/json"
	"strings"
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
	// Migration for file_types unique constraint
	// Check if we need to migrate from UNIQUE(mime_type) to UNIQUE(mime_type, category, subtype)
	// We can check if we can insert a duplicate mime_type (in a transaction that we rollback)
	// Or simpler: check sqlite_master for the table definition
	var sqlStmt string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='file_types'").Scan(&sqlStmt)
	if err == nil {
		// Table exists
		if !strings.Contains(sqlStmt, "UNIQUE(mime_type, category, subtype)") {
			// Old schema detected, migrate
			migrationQueries := []string{
				`CREATE TABLE file_types_new (
					id INTEGER PRIMARY KEY,
					mime_type TEXT,
					category TEXT,
					subtype TEXT,
					UNIQUE(mime_type, category, subtype)
				);`,
				`INSERT INTO file_types_new (id, mime_type, category, subtype) SELECT id, mime_type, category, subtype FROM file_types;`,
				`DROP TABLE file_types;`,
				`ALTER TABLE file_types_new RENAME TO file_types;`,
			}
			for _, q := range migrationQueries {
				if _, err := db.Exec(q); err != nil {
					return err
				}
			}
		}
	}

	queries := []string{
		`CREATE TABLE IF NOT EXISTS file_types (
			id INTEGER PRIMARY KEY,
			mime_type TEXT,
			category TEXT,
			subtype TEXT,
			UNIQUE(mime_type, category, subtype)
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
		`CREATE TABLE IF NOT EXISTS volumes (
			id INTEGER PRIMARY KEY,
			size_total INTEGER DEFAULT 0,
			size_deleted INTEGER DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_files_expires_at ON files(expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_files_old_cumulus_id ON files(old_cumulus_id);`,
		`CREATE INDEX IF NOT EXISTS idx_files_blob_id ON files(blob_id);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_volume_id ON blobs(volume_id);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_id ON blobs(id);`,
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

// tagsToJSON serialises a slice of tag strings to a compact JSON array stored in the DB.
// An empty or nil slice is stored as NULL (empty string).
func tagsToJSON(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// tagsFromJSON deserialises a JSON array column value back to a []string.
// Empty or legacy CSV values are handled gracefully.
func tagsFromJSON(raw string) []string {
	if raw == "" {
		return nil
	}
	// JSON array
	if raw[0] == '[' {
		var tags []string
		if err := json.Unmarshal([]byte(raw), &tags); err == nil {
			return tags
		}
	}
	// Legacy CSV fallback (existing data in the DB before the migration)
	var tags []string
	for _, t := range splitCSV(raw) {
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// splitCSV splits a comma-separated string, trimming whitespace from each part.
func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			for len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			for len(part) > 0 && part[len(part)-1] == ' ' {
				part = part[:len(part)-1]
			}
			out = append(out, part)
			start = i + 1
		}
	}
	return out
}

// TagsToJSON and TagsFromJSON are exported for use outside the storage package.
func TagsToJSON(tags []string) string  { return tagsToJSON(tags) }
func TagsFromJSON(raw string) []string { return tagsFromJSON(raw) }

func (m *MetadataSQL) SaveFile(file File) error {
	query := `
	INSERT INTO files (id, name, blob_id, old_cumulus_id, expires_at, created_at, tags)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := m.db.Exec(query, file.ID, file.Name, file.BlobID, file.OldCumulusID, file.ExpiresAt, file.CreatedAt, file.Tags)
	return err
}

func (m *MetadataSQL) CleanupExpiredFiles() (int64, error) {
	query := `DELETE FROM files WHERE expires_at < datetime('now', 'localtime')`
	res, err := m.db.Exec(query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetExpiredTemporaryFiles returns list of file IDs that have expired.
// The file record will be deleted, but blob deletion is handled by DeleteFile()
// which checks reference count - blob is only deleted if no other files reference it.
func (m *MetadataSQL) GetExpiredTemporaryFiles() ([]string, int, error) {
	query := `
		SELECT id
		FROM files
		WHERE expires_at IS NOT NULL 
		  AND expires_at < datetime('now', 'localtime')
	`

	rows, err := m.db.Query(query)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var fileIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		fileIDs = append(fileIDs, id)
	}

	totalExpired := len(fileIDs)
	return fileIDs, totalExpired, rows.Err()
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

// CreateBlobWithID creates a blob with a specific ID (for database rebuild)
func (m *MetadataSQL) CreateBlobWithID(id int64, hash string) error {
	query := `INSERT INTO blobs (id, hash) VALUES (?, ?)`
	_, err := m.db.Exec(query, id, hash)
	return err
}

// GetDB returns the underlying database connection (for advanced operations)
func (m *MetadataSQL) GetDB() *sql.DB {
	return m.db
}

func (m *MetadataSQL) GetFile(id string) (File, error) {
	var f File
	query := `SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags FROM files WHERE id = ?`
	err := m.db.QueryRow(query, id).Scan(&f.ID, &f.Name, &f.BlobID, &f.OldCumulusID, &f.ExpiresAt, &f.CreatedAt, &f.Tags)
	if err != nil {
		return File{}, err
	}
	return f, nil
}

func (m *MetadataSQL) GetBlob(id int64) (Blob, error) {
	var b Blob
	query := `SELECT id, hash, COALESCE(volume_id, 0), COALESCE(offset, 0), COALESCE(size_raw, 0), COALESCE(size_compressed, 0), COALESCE(compression_alg, ''), COALESCE(file_type_id, 0) FROM blobs WHERE id = ?`
	err := m.db.QueryRow(query, id).Scan(&b.ID, &b.Hash, &b.VolumeID, &b.Offset, &b.SizeRaw, &b.SizeCompressed, &b.CompressionAlg, &b.FileTypeID)
	if err != nil {
		return Blob{}, err
	}
	return b, nil
}

func (m *MetadataSQL) GetFileType(id int64) (FileType, error) {
	var ft FileType
	query := `SELECT id, mime_type, category, subtype FROM file_types WHERE id = ?`
	err := m.db.QueryRow(query, id).Scan(&ft.ID, &ft.MimeType, &ft.Category, &ft.Subtype)
	if err != nil {
		return FileType{}, err
	}
	return ft, nil
}

func (m *MetadataSQL) UpdateBlobLocation(id int64, volumeID, offset, sizeRaw, sizeCompressed int64, compressionAlg string, fileTypeID int64) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
	UPDATE blobs 
	SET volume_id = ?, offset = ?, size_raw = ?, size_compressed = ?, compression_alg = ?, file_type_id = ?
	WHERE id = ?
	`
	if _, err := tx.Exec(query, volumeID, offset, sizeRaw, sizeCompressed, compressionAlg, fileTypeID, id); err != nil {
		return err
	}

	// Note: volumes table is now updated in WriteBlobWithMetadata (inside volume lock)
	// to ensure atomic check-and-update and prevent race conditions

	return tx.Commit()
}

func (m *MetadataSQL) UpdateBlobFileType(blobID int64, fileTypeID int64) error {
	query := `UPDATE blobs SET file_type_id = ? WHERE id = ?`
	_, err := m.db.Exec(query, fileTypeID, blobID)
	return err
}

func (m *MetadataSQL) GetOrCreateFileType(mimeType, category, subtype string) (int64, error) {
	var id int64
	// Try to find exact match first
	err := m.db.QueryRow("SELECT id FROM file_types WHERE mime_type = ? AND category = ? AND subtype = ?", mimeType, category, subtype).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	// If not found, insert new
	res, err := m.db.Exec("INSERT INTO file_types (mime_type, category, subtype) VALUES (?, ?, ?)", mimeType, category, subtype)
	if err != nil {
		// If insert fails (race condition or constraint), try to select again
		err2 := m.db.QueryRow("SELECT id FROM file_types WHERE mime_type = ? AND category = ? AND subtype = ?", mimeType, category, subtype).Scan(&id)
		if err2 == nil {
			return id, nil
		}
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

func (m *MetadataSQL) GetFileByOldID(oldID int64) (File, error) {
	var f File
	query := `SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags FROM files WHERE old_cumulus_id = ?`
	err := m.db.QueryRow(query, oldID).Scan(&f.ID, &f.Name, &f.BlobID, &f.OldCumulusID, &f.ExpiresAt, &f.CreatedAt, &f.Tags)
	if err != nil {
		return File{}, err
	}
	return f, nil
}

// GetMaxOldCumulusID returns the current maximum old_cumulus_id from the files table, or 0 if no rows exist.
func (m *MetadataSQL) GetMaxOldCumulusID() (int64, error) {
	var maxID int64
	err := m.db.QueryRow("SELECT COALESCE(MAX(old_cumulus_id), 0) FROM files").Scan(&maxID)
	return maxID, err
}

// FindFileByBlobNameAndExpiry finds an existing file with the same blob_id, filename, and expiresAt,
// ignoring old_cumulus_id. Used when old_cumulus_id is auto-assigned to avoid creating duplicates.
func (m *MetadataSQL) FindFileByBlobNameAndExpiry(blobID int64, filename string, expiresAt *time.Time) (*File, error) {
	var expAt any
	if expiresAt != nil {
		expAt = *expiresAt
	}

	const query = `SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags
	               FROM files
	               WHERE blob_id = ? AND name = ? AND expires_at IS ?
	               LIMIT 1`

	var f File
	err := m.db.QueryRow(query, blobID, filename, expAt).Scan(
		&f.ID, &f.Name, &f.BlobID, &f.OldCumulusID, &f.ExpiresAt, &f.CreatedAt, &f.Tags)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// FindFileByBlobAndName finds an existing file with the same blob_id, filename, old_cumulus_id, and expiresAt.
// SQLite's IS operator provides null-safe equality, so a single query covers all four nil/non-nil combinations.
func (m *MetadataSQL) FindFileByBlobAndName(blobID int64, filename string, oldCumulusID *int64, expiresAt *time.Time) (*File, error) {
	var oldID any
	if oldCumulusID != nil {
		oldID = *oldCumulusID
	}
	var expAt any
	if expiresAt != nil {
		expAt = *expiresAt
	}

	const query = `SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags
	               FROM files
	               WHERE blob_id = ? AND name = ? AND old_cumulus_id IS ? AND expires_at IS ?
	               LIMIT 1`

	var f File
	err := m.db.QueryRow(query, blobID, filename, oldID, expAt).Scan(
		&f.ID, &f.Name, &f.BlobID, &f.OldCumulusID, &f.ExpiresAt, &f.CreatedAt, &f.Tags)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateFileTags updates the tags for a file.
// tags must be a JSON-encoded array produced by TagsToJSON.
func (m *MetadataSQL) UpdateFileTags(fileID string, tags string) error {
	query := `UPDATE files SET tags = ? WHERE id = ?`
	_, err := m.db.Exec(query, tags, fileID)
	return err
}

// StorageStats holds aggregate statistics returned by GetStorageStats.
type StorageStats struct {
	BlobCount        int64
	BlobTotalSize    int64
	BlobRawSize      int64
	FileCount        int64
	DeletedBlobsSize int64
}

// GetBlobStats returns aggregate counts and sizes from blobs and files tables.
func (m *MetadataSQL) GetBlobStats() (StorageStats, error) {
	var s StorageStats
	err := m.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(size_compressed), 0), COALESCE(SUM(size_raw), 0)
		FROM blobs
	`).Scan(&s.BlobCount, &s.BlobTotalSize, &s.BlobRawSize)
	if err != nil {
		return s, err
	}

	if err := m.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&s.FileCount); err != nil {
		return s, err
	}

	// Blobs not referenced by any file (candidates for compaction).
	err = m.db.QueryRow(`
		SELECT COALESCE(SUM(b.size_compressed), 0)
		FROM blobs b
		LEFT JOIN files f ON b.id = f.blob_id
		WHERE f.blob_id IS NULL
	`).Scan(&s.DeletedBlobsSize)
	if err != nil {
		// Non-fatal – return zeroed field instead of failing the whole request.
		s.DeletedBlobsSize = 0
	}

	return s, nil
}

// IntegrityQuickResult holds counts returned by a quick (DB-only) integrity check.
type IntegrityQuickResult struct {
	OrphanedBlobs int64
	MissingBlobs  int64
}

// GetIntegrityQuick counts orphaned blobs and files referencing non-existent blobs.
func (m *MetadataSQL) GetIntegrityQuick() (IntegrityQuickResult, error) {
	var r IntegrityQuickResult
	err := m.db.QueryRow(`
		SELECT COUNT(*) FROM blobs b
		LEFT JOIN files f ON b.id = f.blob_id
		WHERE f.blob_id IS NULL
	`).Scan(&r.OrphanedBlobs)
	if err != nil {
		return r, err
	}

	err = m.db.QueryRow(`
		SELECT COUNT(*) FROM files f
		LEFT JOIN blobs b ON f.blob_id = b.id
		WHERE b.id IS NULL
	`).Scan(&r.MissingBlobs)
	return r, err
}

// GetDistinctVolumeIDs returns the sorted list of volume IDs referenced by blobs.
func (m *MetadataSQL) GetDistinctVolumeIDs() ([]int64, error) {
	rows, err := m.db.Query(`SELECT DISTINCT volume_id FROM blobs ORDER BY volume_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetBlobsInRange returns a page of blobs ordered by volume_id, offset.
// Used by the deep integrity check to iterate in batches without holding locks.
type BlobLocation struct {
	ID             int64
	VolumeID       int64
	Offset         int64
	SizeCompressed int64
}

func (m *MetadataSQL) GetBlobsInRange(limit, offset int64) ([]BlobLocation, error) {
	rows, err := m.db.Query(`
		SELECT id, volume_id, offset, size_compressed
		FROM blobs
		ORDER BY volume_id, offset
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blobs []BlobLocation
	for rows.Next() {
		var b BlobLocation
		if err := rows.Scan(&b.ID, &b.VolumeID, &b.Offset, &b.SizeCompressed); err != nil {
			return nil, err
		}
		blobs = append(blobs, b)
	}
	return blobs, rows.Err()
}

// GetTotalBlobCount returns the total number of blobs.
func (m *MetadataSQL) GetTotalBlobCount() (int64, error) {
	var count int64
	return count, m.db.QueryRow(`SELECT COUNT(*) FROM blobs`).Scan(&count)
}
