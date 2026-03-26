package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
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

type VolumeInfo struct {
	ID          int
	SizeTotal   int64
	SizeDeleted int64
}

type MetadataSQL struct {
	db     *sql.DB
	dbType string // "sqlite" or "postgresql"
}

// NewMetadataSQL initializes database connection based on type
// dbType: "sqlite" or "postgresql"
// dsn: connection string (DSN for SQLite, connection URL for PostgreSQL)
func NewMetadataSQL(dbType, dsn string) (*MetadataSQL, error) {
	var db *sql.DB
	var err error

	switch dbType {
	case "sqlite":
		db, err = sql.Open("sqlite3", dsn)
		if err != nil {
			return nil, fmt.Errorf("failed to open SQLite: %w", err)
		}
		db.SetMaxOpenConns(1)

	case "postgresql":
		db, err = sql.Open("postgres", dsn)
		if err != nil {
			return nil, fmt.Errorf("failed to open PostgreSQL: %w", err)
		}
		// PostgreSQL can handle more connections
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)

	default:
		return nil, fmt.Errorf("unsupported database type: %s (use 'sqlite' or 'postgresql')", dbType)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	metaSQL := &MetadataSQL{db: db, dbType: dbType}

	if err := metaSQL.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return metaSQL, nil
}

func (m *MetadataSQL) initSchema() error {
	if m.dbType == "sqlite" {
		return m.initSQLiteSchema()
	}
	return m.initPostgreSQLSchema()
}

func (m *MetadataSQL) initSQLiteSchema() error {
	// Migration for file_types unique constraint
	var sqlStmt string
	err := m.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='file_types'").Scan(&sqlStmt)
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
				if _, err := m.db.Exec(q); err != nil {
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
			blob_offset INTEGER,
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
		`CREATE INDEX IF NOT EXISTS idx_files_blob_name_expires ON files(blob_id, name, expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_files_blob_name_old_expires ON files(blob_id, name, old_cumulus_id, expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_volume_id ON blobs(volume_id);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_id ON blobs(id);`,
	}

	for _, query := range queries {
		if _, err := m.db.Exec(query); err != nil {
			return err
		}
	}

	// Migration: Add tags column if not exists
	_, _ = m.db.Exec("ALTER TABLE files ADD COLUMN tags TEXT")

	// Migration: ensure blob_offset column exists on legacy databases
	if err := m.ensureSQLiteBlobOffsetColumn(); err != nil {
		return err
	}

	// Index depending on blob_offset must be created after migration above
	if _, err := m.db.Exec(`CREATE INDEX IF NOT EXISTS idx_blobs_volume_offset ON blobs(volume_id, blob_offset);`); err != nil {
		return err
	}

	return nil
}

func (m *MetadataSQL) sqliteColumnExists(tableName, columnName string) (bool, error) {
	query := fmt.Sprintf("SELECT 1 FROM pragma_table_info('%s') WHERE name = ? LIMIT 1", tableName)
	var exists int
	err := m.db.QueryRow(query, columnName).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (m *MetadataSQL) ensureSQLiteBlobOffsetColumn() error {
	hasBlobOffset, err := m.sqliteColumnExists("blobs", "blob_offset")
	if err != nil {
		return fmt.Errorf("failed to check blobs.blob_offset existence: %w", err)
	}
	if hasBlobOffset {
		return nil
	}

	hasOffset, err := m.sqliteColumnExists("blobs", "offset")
	if err != nil {
		return fmt.Errorf("failed to check blobs.offset existence: %w", err)
	}

	if hasOffset {
		// Prefer proper rename when supported by SQLite.
		if _, err := m.db.Exec("ALTER TABLE blobs RENAME COLUMN offset TO blob_offset"); err == nil {
			return nil
		}

		// Fallback for SQLite variants without RENAME COLUMN support.
		if _, err := m.db.Exec("ALTER TABLE blobs ADD COLUMN blob_offset INTEGER"); err != nil {
			return fmt.Errorf("failed to add blobs.blob_offset column: %w", err)
		}
		if _, err := m.db.Exec("UPDATE blobs SET blob_offset = offset WHERE blob_offset IS NULL"); err != nil {
			return fmt.Errorf("failed to backfill blobs.blob_offset: %w", err)
		}
		return nil
	}

	if _, err := m.db.Exec("ALTER TABLE blobs ADD COLUMN blob_offset INTEGER"); err != nil {
		return fmt.Errorf("failed to add missing blobs.blob_offset column: %w", err)
	}
	return nil
}

func (m *MetadataSQL) initPostgreSQLSchema() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS file_types (
			id BIGSERIAL PRIMARY KEY,
			mime_type VARCHAR(255),
			category VARCHAR(255),
			subtype VARCHAR(255),
			UNIQUE(mime_type, category, subtype)
		);`,
		`CREATE TABLE IF NOT EXISTS blobs (
			id BIGSERIAL PRIMARY KEY,
			hash VARCHAR(128) UNIQUE,
			volume_id BIGINT,
			blob_offset BIGINT,
			size_raw BIGINT,
			size_compressed BIGINT,
			compression_alg VARCHAR(50),
			file_type_id BIGINT,
			FOREIGN KEY(file_type_id) REFERENCES file_types(id)
		);`,
		`CREATE TABLE IF NOT EXISTS files (
			id VARCHAR(255) PRIMARY KEY,
			name TEXT,
			blob_id BIGINT,
			old_cumulus_id BIGINT,
			expires_at TIMESTAMP,
			created_at TIMESTAMP,
			tags TEXT,
			FOREIGN KEY(blob_id) REFERENCES blobs(id)
		);`,
		`CREATE TABLE IF NOT EXISTS volumes (
			id BIGSERIAL PRIMARY KEY,
			size_total BIGINT DEFAULT 0,
			size_deleted BIGINT DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_files_expires_at ON files(expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_files_old_cumulus_id ON files(old_cumulus_id);`,
		`CREATE INDEX IF NOT EXISTS idx_files_blob_id ON files(blob_id);`,
		`CREATE INDEX IF NOT EXISTS idx_files_blob_name_expires ON files(blob_id, name, expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_files_blob_name_old_expires ON files(blob_id, name, old_cumulus_id, expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_volume_id ON blobs(volume_id);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_volume_offset ON blobs(volume_id, blob_offset);`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_id ON blobs(id);`,
	}

	for _, query := range queries {
		if _, err := m.db.Exec(query); err != nil {
			return err
		}
	}

	// Migration: Add tags column if not exists (PostgreSQL safe way)
	_, _ = m.db.Exec(`
		DO $$ 
		BEGIN 
			IF NOT EXISTS (
				SELECT 1 FROM information_schema.columns 
				WHERE table_name='files' AND column_name='tags'
			) THEN 
				ALTER TABLE files ADD COLUMN tags TEXT;
			END IF;
		END $$;
	`)
	// Migration: rename reserved column name offset -> blob_offset if needed
	_, _ = m.db.Exec(`
		DO $$ 
		BEGIN 
			IF EXISTS (
				SELECT 1 FROM information_schema.columns 
				WHERE table_name='blobs' AND column_name='offset'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns 
				WHERE table_name='blobs' AND column_name='blob_offset'
			) THEN 
				ALTER TABLE blobs RENAME COLUMN offset TO blob_offset;
			END IF;
		END $$;
	`)

	return nil
}

func (m *MetadataSQL) Close() error {
	return m.db.Close()
}

// currentTimeSQL returns the appropriate SQL expression for current time based on database type
func (m *MetadataSQL) currentTimeSQL() string {
	if m.dbType == "postgresql" {
		return "NOW()"
	}
	return "datetime('now', 'localtime')"
}

// buildQuery converts ? placeholders to $1, $2, etc. for PostgreSQL
// For SQLite, returns the query unchanged
func (m *MetadataSQL) buildQuery(query string) string {
	if m.dbType != "postgresql" {
		return query
	}

	// Convert ? to $1, $2, $3, etc.
	var result strings.Builder
	paramIndex := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			result.WriteString(fmt.Sprintf("$%d", paramIndex))
			paramIndex++
		} else {
			result.WriteByte(query[i])
		}
	}
	return result.String()
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
	query := m.buildQuery(`
		INSERT INTO files (id, name, blob_id, old_cumulus_id, expires_at, created_at, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	_, err := m.db.Exec(query, file.ID, file.Name, file.BlobID, file.OldCumulusID, file.ExpiresAt, file.CreatedAt, file.Tags)
	return err
}

func (m *MetadataSQL) CleanupExpiredFiles() (int64, error) {
	query := fmt.Sprintf("DELETE FROM files WHERE expires_at < %s", m.currentTimeSQL())
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
	query := fmt.Sprintf(`
		SELECT id
		FROM files
		WHERE expires_at IS NOT NULL
			AND expires_at < %s
	`, m.currentTimeSQL())

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
	query := m.buildQuery(`SELECT id FROM blobs WHERE hash = ?`)
	err := m.db.QueryRow(query, hash).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (m *MetadataSQL) insertAndReturnID(insertQuery string, args ...any) (int64, error) {
	if m.dbType == "postgresql" {
		query := m.buildQuery(insertQuery + ` RETURNING id`)
		var id int64
		if err := m.db.QueryRow(query, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}

	query := m.buildQuery(insertQuery)
	res, err := m.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (m *MetadataSQL) CreateBlob(hash string) (int64, error) {
	return m.insertAndReturnID(`INSERT INTO blobs (hash) VALUES (?)`, hash)
}

// CreateBlobWithID creates a blob with a specific ID (for database rebuild)
func (m *MetadataSQL) CreateBlobWithID(id int64, hash string) error {
	query := m.buildQuery(`INSERT INTO blobs (id, hash) VALUES (?, ?)`)
	_, err := m.db.Exec(query, id, hash)
	return err
}

// GetDB returns the underlying database connection (for advanced operations)
func (m *MetadataSQL) GetDB() *sql.DB {
	return m.db
}

func (m *MetadataSQL) GetFile(id string) (File, error) {
	var f File
	query := m.buildQuery(`SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags FROM files WHERE id = ?`)
	err := m.db.QueryRow(query, id).Scan(&f.ID, &f.Name, &f.BlobID, &f.OldCumulusID, &f.ExpiresAt, &f.CreatedAt, &f.Tags)
	if err != nil {
		return File{}, err
	}
	return f, nil
}

func (m *MetadataSQL) GetBlob(id int64) (Blob, error) {
	var b Blob
	query := m.buildQuery(`SELECT id, hash, COALESCE(volume_id, 0), COALESCE(blob_offset, 0), COALESCE(size_raw, 0), COALESCE(size_compressed, 0), COALESCE(compression_alg, ''), COALESCE(file_type_id, 0) FROM blobs WHERE id = ?`)
	err := m.db.QueryRow(query, id).Scan(&b.ID, &b.Hash, &b.VolumeID, &b.Offset, &b.SizeRaw, &b.SizeCompressed, &b.CompressionAlg, &b.FileTypeID)
	if err != nil {
		return Blob{}, err
	}
	return b, nil
}

func (m *MetadataSQL) GetFileType(id int64) (FileType, error) {
	var ft FileType
	query := m.buildQuery(`SELECT id, mime_type, category, subtype FROM file_types WHERE id = ?`)
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

	query := m.buildQuery(`
	UPDATE blobs 
	SET volume_id = ?, blob_offset = ?, size_raw = ?, size_compressed = ?, compression_alg = ?, file_type_id = ?
	WHERE id = ?
	`)
	if _, err := tx.Exec(query, volumeID, offset, sizeRaw, sizeCompressed, compressionAlg, fileTypeID, id); err != nil {
		return err
	}

	// Note: volumes table is now updated in WriteBlobWithMetadata (inside volume lock)
	// to ensure atomic check-and-update and prevent race conditions

	return tx.Commit()
}

func (m *MetadataSQL) UpdateBlobFileType(blobID int64, fileTypeID int64) error {
	query := m.buildQuery(`UPDATE blobs SET file_type_id = ? WHERE id = ?`)
	_, err := m.db.Exec(query, fileTypeID, blobID)
	return err
}

func (m *MetadataSQL) GetOrCreateFileType(mimeType, category, subtype string) (int64, error) {
	var id int64
	// Try to find exact match first
	query := m.buildQuery("SELECT id FROM file_types WHERE mime_type = ? AND category = ? AND subtype = ?")
	err := m.db.QueryRow(query, mimeType, category, subtype).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	// If not found, insert new
	id, err = m.insertAndReturnID("INSERT INTO file_types (mime_type, category, subtype) VALUES (?, ?, ?)", mimeType, category, subtype)
	if err != nil {
		// If insert fails (race condition or constraint), try to select again
		err2 := m.db.QueryRow(query, mimeType, category, subtype).Scan(&id)
		if err2 == nil {
			return id, nil
		}
		return 0, err
	}
	return id, nil
}

func (m *MetadataSQL) FileExistsByOldID(oldID int64) (bool, error) {
	var count int
	query := m.buildQuery("SELECT count(*) FROM files WHERE old_cumulus_id = ?")
	err := m.db.QueryRow(query, oldID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (m *MetadataSQL) GetFileByOldID(oldID int64) (File, error) {
	var f File
	query := m.buildQuery(`SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags FROM files WHERE old_cumulus_id = ?`)
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

	query := m.buildQuery(`SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags
					FROM files
					WHERE blob_id = ? AND name = ? AND expires_at IS ?
					LIMIT 1`)

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

	query := m.buildQuery(`SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags
					FROM files
					WHERE blob_id = ? AND name = ? AND old_cumulus_id IS ? AND expires_at IS ?
					LIMIT 1`)

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
	query := m.buildQuery(`UPDATE files SET tags = ? WHERE id = ?`)
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

// GetBlobsInRange returns a page of blobs ordered by volume_id, blob_offset.
// Used by the deep integrity check to iterate in batches without holding locks.
type BlobLocation struct {
	ID             int64
	VolumeID       int64
	Offset         int64
	SizeCompressed int64
}

type BlobCompactionRecord struct {
	ID             int64
	Hash           string
	Offset         int64
	SizeCompressed int64
}

type BlobMetaRecord struct {
	ID             int64
	Offset         int64
	SizeCompressed int64
	CompressionAlg string
}

type VolumeCompactionTx struct {
	m          *MetadataSQL
	tx         *sql.Tx
	updateStmt *sql.Stmt
}

func (m *MetadataSQL) GetBlobsInRange(limit, offset int64) ([]BlobLocation, error) {
	query := m.buildQuery(`
		SELECT id, volume_id, blob_offset, size_compressed
		FROM blobs
		ORDER BY volume_id, blob_offset
		LIMIT ? OFFSET ?
	`)
	rows, err := m.db.Query(query, limit, offset)
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

func (m *MetadataSQL) GetVolumeSize(volumeID int64) (int64, error) {
	var currentSize int64
	query := m.buildQuery("SELECT COALESCE(size_total, 0) FROM volumes WHERE id = ?")
	err := m.db.QueryRow(query, volumeID).Scan(&currentSize)
	return currentSize, err
}

func (m *MetadataSQL) AddWrittenBytesToVolume(volumeID int64, bytes int64) error {
	volQuery := m.buildQuery(`
		INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, ?, 0)
		ON CONFLICT(id) DO UPDATE SET size_total = size_total + ?
	`)
	_, err := m.db.Exec(volQuery, volumeID, bytes, bytes)
	return err
}

func (m *MetadataSQL) GetBlobsForCompaction(volumeID int64) ([]BlobCompactionRecord, error) {
	query := m.buildQuery("SELECT id, hash, blob_offset, size_compressed FROM blobs WHERE volume_id = ? ORDER BY blob_offset ASC")
	rows, err := m.db.Query(query, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blobs []BlobCompactionRecord
	for rows.Next() {
		var b BlobCompactionRecord
		if err := rows.Scan(&b.ID, &b.Hash, &b.Offset, &b.SizeCompressed); err != nil {
			return nil, err
		}
		blobs = append(blobs, b)
	}
	return blobs, rows.Err()
}

func (m *MetadataSQL) GetBlobsForMetaRegeneration(volumeID int64) ([]BlobMetaRecord, error) {
	query := m.buildQuery(`
		SELECT id, blob_offset, size_compressed, compression_alg
		FROM blobs
		WHERE volume_id = ?
		ORDER BY blob_offset ASC
	`)
	rows, err := m.db.Query(query, volumeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blobs []BlobMetaRecord
	for rows.Next() {
		var b BlobMetaRecord
		if err := rows.Scan(&b.ID, &b.Offset, &b.SizeCompressed, &b.CompressionAlg); err != nil {
			return nil, err
		}
		blobs = append(blobs, b)
	}
	return blobs, rows.Err()
}

func (m *MetadataSQL) BeginVolumeCompactionTx() (*VolumeCompactionTx, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return nil, err
	}

	updateQuery := m.buildQuery("UPDATE blobs SET blob_offset = ? WHERE id = ?")
	updateStmt, err := tx.Prepare(updateQuery)
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	return &VolumeCompactionTx{m: m, tx: tx, updateStmt: updateStmt}, nil
}

func (c *VolumeCompactionTx) UpdateBlobOffset(blobID, newOffset int64) error {
	_, err := c.updateStmt.Exec(newOffset, blobID)
	return err
}

func (c *VolumeCompactionTx) UpdateVolumeSize(volumeID, sizeTotal int64) error {
	query := c.m.buildQuery("UPDATE volumes SET size_total = ?, size_deleted = 0 WHERE id = ?")
	_, err := c.tx.Exec(query, sizeTotal, volumeID)
	return err
}

func (c *VolumeCompactionTx) Commit() error {
	if c.updateStmt != nil {
		_ = c.updateStmt.Close()
		c.updateStmt = nil
	}
	return c.tx.Commit()
}

func (c *VolumeCompactionTx) Rollback() error {
	if c.updateStmt != nil {
		_ = c.updateStmt.Close()
		c.updateStmt = nil
	}
	return c.tx.Rollback()
}

func (m *MetadataSQL) IncrementDeletedSize(volumeID int64, bytes int64) error {
	query := m.buildQuery(`
INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, 0, ?)
ON CONFLICT(id) DO UPDATE SET size_deleted = size_deleted + ?
`)
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
		rows, err = m.db.Query(m.buildQuery(query), thresholdRatio)
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
	query := m.buildQuery("SELECT blob_id FROM files WHERE id = ?")
	err = tx.QueryRow(query, fileID).Scan(&blobID)
	if err == sql.ErrNoRows {
		return nil // File doesn't exist, nothing to do
	}
	if err != nil {
		return err
	}

	// Delete file
	deleteQuery := m.buildQuery("DELETE FROM files WHERE id = ?")
	if _, err = tx.Exec(deleteQuery, fileID); err != nil {
		return err
	}

	// Check ref count
	var count int
	countQuery := m.buildQuery("SELECT count(*) FROM files WHERE blob_id = ?")
	err = tx.QueryRow(countQuery, blobID).Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		// Blob is no longer referenced.
		// Get blob info to know volume and size
		var volumeID, sizeCompressed int64
		blobQuery := m.buildQuery("SELECT volume_id, size_compressed FROM blobs WHERE id = ?")
		err = tx.QueryRow(blobQuery, blobID).Scan(&volumeID, &sizeCompressed)
		if err != nil {
			return err
		}

		// Calculate total size (Header + Compressed + Footer)
		totalSize := int64(HeaderSize) + sizeCompressed + int64(FooterSize)

		// Update volumes table
		volQuery := m.buildQuery(`
INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, 0, ?)
ON CONFLICT(id) DO UPDATE SET size_deleted = size_deleted + ?
`)
		if _, err = tx.Exec(volQuery, volumeID, totalSize, totalSize); err != nil {
			return err
		}

		// Delete the blob record so it's not copied during compaction
		deleteBlobQuery := m.buildQuery("DELETE FROM blobs WHERE id = ?")
		if _, err = tx.Exec(deleteBlobQuery, blobID); err != nil {
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
