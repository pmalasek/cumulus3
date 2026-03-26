package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/pmalasek/cumulus3/src/internal/storage"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

const defaultBatchSize = 5000

type migrator struct {
	src          *sql.DB
	dst          *sql.DB
	batchSize    int
	progressStep int64
}

func main() {
	_ = godotenv.Load()

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "SQLite to PostgreSQL Migration Tool for Cumulus3\n\n")
		fmt.Fprintf(os.Stderr, "Copies metadata tables from SQLite to PostgreSQL while preserving IDs and relationships.\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  -sqlite-path string\n")
		fmt.Fprintf(os.Stderr, "        Path to source SQLite database (default: DB_SQLITE_PATH or ./data/database/cumulus3.db)\n")
		fmt.Fprintf(os.Stderr, "  -pg-url string\n")
		fmt.Fprintf(os.Stderr, "        Target PostgreSQL DSN (default: PG_DATABASE_URL)\n")
		fmt.Fprintf(os.Stderr, "  -batch-size int\n")
		fmt.Fprintf(os.Stderr, "        Number of rows per PostgreSQL transaction batch (default: %d)\n", defaultBatchSize)
		fmt.Fprintf(os.Stderr, "  -truncate\n")
		fmt.Fprintf(os.Stderr, "        Truncate target tables before import\n")
		fmt.Fprintf(os.Stderr, "  -progress int\n")
		fmt.Fprintf(os.Stderr, "        Print progress every N rows (default: 10000)\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s -sqlite-path ./data/database/cumulus3.db -pg-url postgresql://user:pass@localhost:5432/cumulus3?sslmode=disable\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -truncate\n", os.Args[0])
	}

	sqlitePath := flag.String("sqlite-path", envOrDefault("DB_SQLITE_PATH", "./data/database/cumulus3.db"), "Path to source SQLite DB")
	pgURL := flag.String("pg-url", os.Getenv("PG_DATABASE_URL"), "Target PostgreSQL DSN")
	batchSize := flag.Int("batch-size", defaultBatchSize, "Rows per transaction batch")
	truncate := flag.Bool("truncate", false, "Truncate target tables before import")
	progress := flag.Int64("progress", 10000, "Progress reporting interval in rows")
	flag.Parse()

	if strings.TrimSpace(*pgURL) == "" {
		flag.Usage()
		log.Fatal("-pg-url or PG_DATABASE_URL is required")
	}
	if *batchSize <= 0 {
		log.Fatal("-batch-size must be greater than 0")
	}
	if *progress <= 0 {
		*progress = int64(*batchSize)
	}

	sqliteDSN := sqlitePathToDSN(*sqlitePath)
	src, err := sql.Open("sqlite3", sqliteDSN)
	if err != nil {
		log.Fatalf("failed to open SQLite: %v", err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)

	if err := src.Ping(); err != nil {
		log.Fatalf("failed to ping SQLite: %v", err)
	}

	meta, err := storage.NewMetadataSQL("postgresql", *pgURL)
	if err != nil {
		log.Fatalf("failed to open PostgreSQL: %v", err)
	}
	defer meta.Close()

	dst := meta.GetDB()

	if *truncate {
		fmt.Println("⚠️  Truncating target PostgreSQL tables before migration...")
		if err := truncateDestination(dst); err != nil {
			log.Fatalf("failed to truncate destination tables: %v", err)
		}
	} else {
		hasData, err := destinationHasData(dst)
		if err != nil {
			log.Fatalf("failed to inspect destination tables: %v", err)
		}
		if hasData {
			log.Fatal("destination PostgreSQL database is not empty; use -truncate or start with an empty database")
		}
	}

	m := &migrator{
		src:          src,
		dst:          dst,
		batchSize:    *batchSize,
		progressStep: *progress,
	}

	start := time.Now()
	fmt.Println("🚚 Starting SQLite → PostgreSQL migration...")
	fmt.Printf("   SQLite: %s\n", *sqlitePath)
	fmt.Printf("   PostgreSQL: %s\n", maskDSN(*pgURL))
	fmt.Printf("   Batch size: %d\n\n", *batchSize)

	stats := []struct {
		name string
		run  func() (int64, error)
	}{
		{name: "file_types", run: m.migrateFileTypes},
		{name: "blobs", run: m.migrateBlobs},
		{name: "files", run: m.migrateFiles},
		{name: "volumes", run: m.migrateVolumes},
	}

	for _, step := range stats {
		copied, err := step.run()
		if err != nil {
			log.Fatalf("migration failed for %s: %v", step.name, err)
		}
		fmt.Printf("✅ %s migrated: %d rows\n", step.name, copied)
	}

	if err := resetSequences(dst); err != nil {
		log.Fatalf("failed to reset PostgreSQL sequences: %v", err)
	}

	if err := verifyCounts(src, dst); err != nil {
		log.Fatalf("row count verification failed: %v", err)
	}

	fmt.Printf("\n🎉 Migration completed successfully in %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Println("   Row counts match between SQLite and PostgreSQL.")
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func sqlitePathToDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		return path
	}
	return fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_sync=NORMAL", path)
}

func maskDSN(dsn string) string {
	at := strings.Index(dsn, "@")
	proto := strings.Index(dsn, "://")
	if at == -1 || proto == -1 || at <= proto+3 {
		return dsn
	}
	return dsn[:proto+3] + "****:****" + dsn[at:]
}

func truncateDestination(dst *sql.DB) error {
	_, err := dst.Exec(`TRUNCATE TABLE files, blobs, file_types, volumes RESTART IDENTITY CASCADE`)
	return err
}

func destinationHasData(dst *sql.DB) (bool, error) {
	for _, table := range []string{"file_types", "blobs", "files", "volumes"} {
		count, err := countRows(dst, table)
		if err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func countRows(db *sql.DB, table string) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func verifyCounts(src, dst *sql.DB) error {
	for _, table := range []string{"file_types", "blobs", "files", "volumes"} {
		srcCount, err := countRows(src, table)
		if err != nil {
			return fmt.Errorf("source count for %s failed: %w", table, err)
		}
		dstCount, err := countRows(dst, table)
		if err != nil {
			return fmt.Errorf("destination count for %s failed: %w", table, err)
		}
		if srcCount != dstCount {
			return fmt.Errorf("table %s count mismatch: sqlite=%d postgresql=%d", table, srcCount, dstCount)
		}
	}
	return nil
}

func resetSequences(dst *sql.DB) error {
	queries := []string{
		`SELECT setval(pg_get_serial_sequence('file_types', 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM file_types`,
		`SELECT setval(pg_get_serial_sequence('blobs', 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM blobs`,
		`SELECT setval(pg_get_serial_sequence('volumes', 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM volumes`,
	}
	for _, query := range queries {
		if _, err := dst.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (m *migrator) migrateFileTypes() (int64, error) {
	rows, err := m.src.Query(`SELECT id, mime_type, category, subtype FROM file_types ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	insertSQL := `INSERT INTO file_types (id, mime_type, category, subtype) VALUES ($1, $2, $3, $4)`
	return m.copyRows("file_types", rows, insertSQL, func(stmt *sql.Stmt) error {
		var id int64
		var mimeType, category, subtype sql.NullString
		if err := rows.Scan(&id, &mimeType, &category, &subtype); err != nil {
			return err
		}
		_, err := stmt.Exec(id, mimeType, category, subtype)
		return err
	})
}

func (m *migrator) migrateBlobs() (int64, error) {
	rows, err := m.src.Query(`
		SELECT id, hash, volume_id, blob_offset, size_raw, size_compressed, compression_alg, file_type_id
		FROM blobs
		ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	insertSQL := `
		INSERT INTO blobs (id, hash, volume_id, blob_offset, size_raw, size_compressed, compression_alg, file_type_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	return m.copyRows("blobs", rows, insertSQL, func(stmt *sql.Stmt) error {
		var id int64
		var hash, compressionAlg sql.NullString
		var volumeID, blobOffset, sizeRaw, sizeCompressed, fileTypeID sql.NullInt64
		if err := rows.Scan(&id, &hash, &volumeID, &blobOffset, &sizeRaw, &sizeCompressed, &compressionAlg, &fileTypeID); err != nil {
			return err
		}
		_, err := stmt.Exec(id, hash, volumeID, blobOffset, sizeRaw, sizeCompressed, compressionAlg, fileTypeID)
		return err
	})
}

func (m *migrator) migrateFiles() (int64, error) {
	rows, err := m.src.Query(`
		SELECT id, name, blob_id, old_cumulus_id, expires_at, created_at, tags
		FROM files
		ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	insertSQL := `
		INSERT INTO files (id, name, blob_id, old_cumulus_id, expires_at, created_at, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	return m.copyRows("files", rows, insertSQL, func(stmt *sql.Stmt) error {
		var id string
		var name, tags sql.NullString
		var blobID, oldCumulusID sql.NullInt64
		var expiresAtRaw, createdAtRaw any

		if err := rows.Scan(&id, &name, &blobID, &oldCumulusID, &expiresAtRaw, &createdAtRaw, &tags); err != nil {
			return err
		}

		expiresAt, err := normalizeTimeValue(expiresAtRaw, true)
		if err != nil {
			return fmt.Errorf("expires_at for file %s: %w", id, err)
		}
		createdAt, err := normalizeTimeValue(createdAtRaw, false)
		if err != nil {
			return fmt.Errorf("created_at for file %s: %w", id, err)
		}

		_, err = stmt.Exec(id, name, blobID, oldCumulusID, expiresAt, createdAt, tags)
		return err
	})
}

func (m *migrator) migrateVolumes() (int64, error) {
	rows, err := m.src.Query(`SELECT id, size_total, size_deleted FROM volumes ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	insertSQL := `INSERT INTO volumes (id, size_total, size_deleted) VALUES ($1, $2, $3)`
	return m.copyRows("volumes", rows, insertSQL, func(stmt *sql.Stmt) error {
		var id, sizeTotal, sizeDeleted int64
		if err := rows.Scan(&id, &sizeTotal, &sizeDeleted); err != nil {
			return err
		}
		_, err := stmt.Exec(id, sizeTotal, sizeDeleted)
		return err
	})
}

func (m *migrator) copyRows(table string, rows *sql.Rows, insertSQL string, insertFn func(stmt *sql.Stmt) error) (int64, error) {
	fmt.Printf("→ Migrating %s...\n", table)

	tx, stmt, err := m.beginBatch(insertSQL)
	if err != nil {
		return 0, err
	}

	processed := int64(0)
	batchCount := 0
	commit := func() error {
		if stmt != nil {
			if err := stmt.Close(); err != nil {
				return err
			}
			stmt = nil
		}
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return err
			}
			tx = nil
		}
		return nil
	}
	rollback := func() {
		if stmt != nil {
			_ = stmt.Close()
			stmt = nil
		}
		if tx != nil {
			_ = tx.Rollback()
			tx = nil
		}
	}

	for rows.Next() {
		if err := insertFn(stmt); err != nil {
			rollback()
			return processed, err
		}
		processed++
		batchCount++

		if processed%m.progressStep == 0 {
			fmt.Printf("   %s: %d rows\n", table, processed)
		}

		if batchCount >= m.batchSize {
			if err := commit(); err != nil {
				rollback()
				return processed, err
			}
			tx, stmt, err = m.beginBatch(insertSQL)
			if err != nil {
				return processed, err
			}
			batchCount = 0
		}
	}

	if err := rows.Err(); err != nil {
		rollback()
		return processed, err
	}

	if err := commit(); err != nil {
		rollback()
		return processed, err
	}

	return processed, nil
}

func (m *migrator) beginBatch(insertSQL string) (*sql.Tx, *sql.Stmt, error) {
	tx, err := m.dst.Begin()
	if err != nil {
		return nil, nil, err
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}
	return tx, stmt, nil
}

func normalizeTimeValue(raw any, allowNull bool) (any, error) {
	switch v := raw.(type) {
	case nil:
		if allowNull {
			return nil, nil
		}
		return nil, errors.New("required timestamp is NULL")
	case time.Time:
		return v, nil
	case string:
		return parseSQLiteTimeText(v, allowNull)
	case []byte:
		return parseSQLiteTimeText(string(v), allowNull)
	case int64:
		return time.Unix(v, 0), nil
	case float64:
		return time.Unix(int64(v), 0), nil
	default:
		return nil, fmt.Errorf("unsupported timestamp type %T", raw)
	}
}

func parseSQLiteTimeText(s string, allowNull bool) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if allowNull {
			return nil, nil
		}
		return nil, errors.New("required timestamp is empty")
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	if unixSeconds, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(unixSeconds, 0), nil
	}

	return s, nil
}
