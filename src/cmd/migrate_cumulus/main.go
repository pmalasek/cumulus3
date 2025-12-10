package main

import (
	"compress/bzip2"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	"github.com/pmalasek/cumulus3/src/internal/service"
	"github.com/pmalasek/cumulus3/src/internal/storage"
	"github.com/pmalasek/cumulus3/src/internal/utils"
)

func main() {
	// Load .env file if exists, for destination config
	_ = godotenv.Load()

	// Flags
	dbHost := flag.String("db-host", "", "Database host IP")
	dbPort := flag.Int("db-port", 3306, "Database port")
	dbUser := flag.String("db-user", "", "Database user")
	dbPass := flag.String("db-pass", "", "Database password")
	dbName := flag.String("db-name", "", "Database name")
	filesPath := flag.String("files-path", "", "Path to source files")

	flag.Parse()

	if *dbHost == "" || *dbUser == "" || *dbName == "" || *filesPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	// Connect to Source MySQL
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", *dbUser, *dbPass, *dbHost, *dbPort, *dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("Error connecting to MySQL: %v", err)
	}
	// defer db.Close() // We will close it manually after reading rows

	if err := db.Ping(); err != nil {
		log.Fatalf("Error pinging MySQL: %v", err)
	}

	// Initialize Destination (Cumulus3)
	// Using defaults or env vars similar to volume-server
	destDbPath := os.Getenv("DB_PATH")
	if destDbPath == "" {
		destDbPath = "./data/database/cumulus3.db"
	}

	destDataDir := os.Getenv("DATA_DIR")
	if destDataDir == "" {
		destDataDir = "./data"
	}

	dataFileSizeStr := os.Getenv("DATA_FILE_SIZE")
	var maxDataFileSize int64 = 10 << 20 // Default 10MB
	if dataFileSizeStr != "" {
		if s, err := utils.ParseBytes(dataFileSizeStr); err == nil {
			maxDataFileSize = s
		}
	}

	// Ensure directories exist
	os.MkdirAll(filepath.Dir(destDbPath), 0755)
	os.MkdirAll(destDataDir, 0755)

	// Initialize Metadata Store
	metaStore, err := storage.NewMetadataSQL(fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=NORMAL", destDbPath))
	if err != nil {
		log.Fatalf("Error initializing metadata store: %v", err)
	}
	defer metaStore.Close()

	// Initialize File Store
	fileStore := storage.NewStore(destDataDir, maxDataFileSize)

	// Initialize Logger
	metaLogger := storage.NewMetadataLogger(destDataDir)

	// Initialize Service
	// Compression defaults
	compressionMode := os.Getenv("USE_COMPRESS")
	if compressionMode == "" {
		compressionMode = "Auto"
	}
	minCompressionRatio := 10.0

	fileService := service.NewFileService(fileStore, metaStore, metaLogger, compressionMode, minCompressionRatio)

	// Execute Query
	query := `
        SELECT
            f.id,
            f.files_id,
            f.filename,
            rf.id as raw_id,
            rf.file_type as raw_file_type,
            group_concat(l.label) as labels
        FROM filenames f
            LEFT JOIN raw_files rf ON rf.id = f.files_id
            LEFT JOIN link_filenames_labels lfl ON lfl.filename_id = f.id
                LEFT JOIN labels l ON l.id = lfl.label_id
        where rf.id is not null
        group by f.id
        ORDER BY f.id ASC
        limit 10000;
    `

	rows, err := db.Query(query)
	if err != nil {
		log.Fatalf("Error executing query: %v", err)
	}

	type MigrationFile struct {
		FID      int64
		Filename string
		RawID    int64
		Tags     string
	}

	var filesToMigrate []MigrationFile

	for rows.Next() {
		var fID int64
		var filesID sql.NullInt64
		var filename string
		var rawID int64
		var rawFileType sql.NullString
		var labels sql.NullString

		if err := rows.Scan(&fID, &filesID, &filename, &rawID, &rawFileType, &labels); err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}

		tags := ""
		if labels.Valid {
			tags = labels.String
		}

		filesToMigrate = append(filesToMigrate, MigrationFile{
			FID:      fID,
			Filename: filename,
			RawID:    rawID,
			Tags:     tags,
		})
	}
	rows.Close()
	db.Close() // Close DB connection immediately after reading

	log.Printf("Loaded %d files to migrate. Starting migration...", len(filesToMigrate))

	count := 0
	for _, mFile := range filesToMigrate {
		// Check if already migrated
		exists, err := metaStore.FileExistsByOldID(mFile.FID)
		if err != nil {
			log.Printf("Error checking existence for ID %d: %v", mFile.FID, err)
			continue
		}
		if exists {
			log.Printf("Skipping file %s (ID: %d) - already migrated", mFile.Filename, mFile.FID)
			continue
		}

		// Calculate path
		roundedID := roundToThousands(mFile.RawID)
		inputFileName := getInputFileName(mFile.RawID)
		fullPath := filepath.Join(*filesPath, fmt.Sprintf("%d", roundedID), inputFileName)

		// Check if source file exists
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			log.Printf("Source file not found: %s", fullPath)
			continue
		}

		// Read file
		file, err := os.Open(fullPath)
		if err != nil {
			log.Printf("Error opening file %s: %v", fullPath, err)
			continue
		}

		// Decompress BZ2
		bz2Reader := bzip2.NewReader(file)

		// Note: UploadFile expects oldCumulusID as *int64
		oldID := mFile.FID

		// Sanitize filename (remove path)
		cleanFilename := filepath.Base(mFile.Filename)

		log.Printf("Migrating file: %s (ID: %d, RawID: %d)", cleanFilename, mFile.FID, mFile.RawID)

		_, err = fileService.UploadFile(bz2Reader, cleanFilename, "", &oldID, nil, mFile.Tags)
		file.Close() // Close file after upload

		if err != nil {
			log.Printf("Error uploading file %s: %v", mFile.Filename, err)
		} else {
			count++
		}
	}

	log.Printf("Migration completed. Migrated %d files.", count)
}

func roundToThousands(num int64) int64 {
	return (num / 1000) * 1000
}

func getInputFileName(id int64) string {
	return fmt.Sprintf("%010d.bz2", id)
}
