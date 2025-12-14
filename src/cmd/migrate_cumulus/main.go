package main

import (
	"bytes"
	"compress/bzip2"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

type MigrationFile struct {
	FID         int64
	Filename    string
	RawID       int64
	Tags        string
	ContentType string
}

type TestMismatch struct {
	CumulusID int64  `json:"cumulus_id"`
	Filename  string `json:"filename"`
	Status    string `json:"status"` // "missing", "hash_mismatch"
	OldHash   string `json:"old_hash,omitempty"`
	NewHash   string `json:"new_hash,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	// Load .env file if exists
	_ = godotenv.Load()

	// Custom usage function
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Cumulus Migration Tool - Migrate files from old Cumulus to new Cumulus via API\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Required Options:\n")
		fmt.Fprintf(os.Stderr, "  -db-host string\n")
		fmt.Fprintf(os.Stderr, "        Source MySQL database host IP address\n")
		fmt.Fprintf(os.Stderr, "  -db-user string\n")
		fmt.Fprintf(os.Stderr, "        Source MySQL database username\n")
		fmt.Fprintf(os.Stderr, "  -db-name string\n")
		fmt.Fprintf(os.Stderr, "        Source MySQL database name\n")
		fmt.Fprintf(os.Stderr, "  -files-path string\n")
		fmt.Fprintf(os.Stderr, "        Path to source files directory\n\n")
		fmt.Fprintf(os.Stderr, "Optional Database Options:\n")
		fmt.Fprintf(os.Stderr, "  -db-port int\n")
		fmt.Fprintf(os.Stderr, "        Source MySQL database port (default: 3306)\n")
		fmt.Fprintf(os.Stderr, "  -db-pass string\n")
		fmt.Fprintf(os.Stderr, "        Source MySQL database password\n\n")
		fmt.Fprintf(os.Stderr, "API Options:\n")
		fmt.Fprintf(os.Stderr, "  -api-host string\n")
		fmt.Fprintf(os.Stderr, "        Cumulus API server host/IP (default: localhost)\n")
		fmt.Fprintf(os.Stderr, "  -api-port int\n")
		fmt.Fprintf(os.Stderr, "        Cumulus API server port (default: 8080)\n\n")
		fmt.Fprintf(os.Stderr, "Performance Options:\n")
		fmt.Fprintf(os.Stderr, "  -workers int\n")
		fmt.Fprintf(os.Stderr, "        Number of parallel workers for migration (default: 10)\n")
		fmt.Fprintf(os.Stderr, "  -limit int\n")
		fmt.Fprintf(os.Stderr, "        Maximum number of files to migrate (default: 10000)\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s -db-host 192.168.1.100 -db-user cumulus -db-name cumulus_old -files-path /mnt/files\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -db-host localhost -db-user root -db-pass secret -db-name cumulus \\\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "    -files-path /data/files -api-host cumulus.local -api-port 8080 -workers 20\n\n")
	}

	// Flags
	dbHost := flag.String("db-host", "", "Database host IP")
	dbPort := flag.Int("db-port", 3306, "Database port")
	dbUser := flag.String("db-user", "", "Database user")
	dbPass := flag.String("db-pass", "", "Database password")
	dbName := flag.String("db-name", "", "Database name")
	filesPath := flag.String("files-path", "", "Path to source files")

	// New flags for API
	apiHost := flag.String("api-host", "localhost", "Cumulus API host IP")
	apiPort := flag.Int("api-port", 8080, "Cumulus API port")
	workers := flag.Int("workers", 10, "Number of parallel workers")
	limit := flag.Int("limit", 10000, "Maximum number of files to migrate")
	testOnly := flag.Bool("test-only", false, "Test mode: compare old and new Cumulus without migration")

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

	// Build API URL
	apiURL := fmt.Sprintf("http://%s:%d/v2/files/upload", *apiHost, *apiPort)

	// Execute Query
	query := fmt.Sprintf(`
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
        LIMIT %d;
    `, *limit)

	rows, err := db.Query(query)
	if err != nil {
		log.Fatalf("Error executing query: %v", err)
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

		contentType := ""
		if rawFileType.Valid {
			contentType = rawFileType.String
		}

		filesToMigrate = append(filesToMigrate, MigrationFile{
			FID:         fID,
			Filename:    filename,
			RawID:       rawID,
			Tags:        tags,
			ContentType: contentType,
		})
	}
	rows.Close()
	db.Close() // Close DB connection immediately after reading

	if *testOnly {
		log.Printf("Loaded %d files to test. Starting test mode with %d workers...", len(filesToMigrate), *workers)
	} else {
		log.Printf("Loaded %d files to migrate. Starting migration with %d workers...", len(filesToMigrate), *workers)
	}

	// Create HTTP client with connection pooling
	httpClient := &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        *workers,
			MaxIdleConnsPerHost: *workers,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Parallel processing
	var (
		successCount int64
		errorCount   int64
		wg           sync.WaitGroup
		jobs         = make(chan MigrationFile, *workers*2)
		mismatches   []TestMismatch
		mismatchMux  sync.Mutex
	)

	// Start workers
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for mFile := range jobs {
				if *testOnly {
					if mismatch := testFile(httpClient, *apiHost, *apiPort, *filesPath, mFile); mismatch != nil {
						mismatchMux.Lock()
						mismatches = append(mismatches, *mismatch)
						mismatchMux.Unlock()
						log.Printf("[Worker %d] MISMATCH: %s (ID: %d) - %s", workerID, mFile.Filename, mFile.FID, mismatch.Status)
						atomic.AddInt64(&errorCount, 1)
					} else {
						log.Printf("[Worker %d] MATCH: %s (ID: %d)", workerID, mFile.Filename, mFile.FID)
						atomic.AddInt64(&successCount, 1)
					}
				} else {
					if err := migrateFile(httpClient, apiURL, *filesPath, mFile); err != nil {
						log.Printf("[Worker %d] ERROR: %s (ID: %d) - %v", workerID, mFile.Filename, mFile.FID, err)
						atomic.AddInt64(&errorCount, 1)
					} else {
						log.Printf("[Worker %d] SUCCESS: %s (ID: %d)", workerID, mFile.Filename, mFile.FID)
						atomic.AddInt64(&successCount, 1)
					}
				}
			}
		}(i)
	}

	// Feed jobs
	startTime := time.Now()
	for _, mFile := range filesToMigrate {
		jobs <- mFile
	}
	close(jobs)

	// Wait for completion
	wg.Wait()

	elapsed := time.Since(startTime)

	if *testOnly {
		log.Printf("Test completed in %s. Matches: %d, Mismatches: %d, Total: %d",
			elapsed, successCount, errorCount, len(filesToMigrate))

		// Write mismatches to JSON file
		if len(mismatches) > 0 {
			outputFile := fmt.Sprintf("mismatches_%s.json", time.Now().Format("20060102_150405"))
			if err := saveMismatchesToJSON(mismatches, outputFile); err != nil {
				log.Printf("Error saving mismatches to JSON: %v", err)
			} else {
				log.Printf("Mismatches saved to: %s", outputFile)
			}
		} else {
			log.Printf("No mismatches found! All files match.")
		}
	} else {
		log.Printf("Migration completed in %s. Success: %d, Errors: %d, Total: %d",
			elapsed, successCount, errorCount, len(filesToMigrate))
	}
}

func roundToThousands(num int64) int64 {
	return (num / 1000) * 1000
}

func getInputFileName(id int64) string {
	return fmt.Sprintf("%010d.bz2", id)
}

// migrateFile migrates a single file via API
func migrateFile(client *http.Client, apiURL, filesPath string, mFile MigrationFile) error {
	// Calculate source file path
	roundedID := roundToThousands(mFile.RawID)
	inputFileName := getInputFileName(mFile.RawID)
	fullPath := filepath.Join(filesPath, fmt.Sprintf("%d", roundedID), inputFileName)

	// Check if source file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fmt.Errorf("source file not found: %s", fullPath)
	}

	// Read and decompress file
	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	bz2Reader := bzip2.NewReader(file)

	// Read decompressed content into memory
	decompressedData, err := io.ReadAll(bz2Reader)
	if err != nil {
		return fmt.Errorf("error decompressing file: %w", err)
	}

	// Prepare multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file
	cleanFilename := filepath.Base(mFile.Filename)
	part, err := writer.CreateFormFile("file", cleanFilename)
	if err != nil {
		return fmt.Errorf("error creating form file: %w", err)
	}

	if _, err := io.Copy(part, bytes.NewReader(decompressedData)); err != nil {
		return fmt.Errorf("error writing file data: %w", err)
	}

	// Add old_cumulus_id
	if err := writer.WriteField("old_cumulus_id", strconv.FormatInt(mFile.FID, 10)); err != nil {
		return fmt.Errorf("error writing old_cumulus_id: %w", err)
	}

	// Add tags if present
	if mFile.Tags != "" {
		if err := writer.WriteField("tags", mFile.Tags); err != nil {
			return fmt.Errorf("error writing tags: %w", err)
		}
	}

	writer.Close()

	// Create request
	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// testFile compares file from old Cumulus with new Cumulus via API
func testFile(client *http.Client, apiHost string, apiPort int, filesPath string, mFile MigrationFile) *TestMismatch {
	// Load and decompress old file
	roundedID := roundToThousands(mFile.RawID)
	inputFileName := getInputFileName(mFile.RawID)
	fullPath := filepath.Join(filesPath, fmt.Sprintf("%d", roundedID), inputFileName)

	// Check if source file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			Error:     fmt.Sprintf("source file not found: %s", fullPath),
		}
	}

	// Read and decompress file
	file, err := os.Open(fullPath)
	if err != nil {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			Error:     fmt.Sprintf("error opening file: %v", err),
		}
	}
	defer file.Close()

	bz2Reader := bzip2.NewReader(file)
	oldData, err := io.ReadAll(bz2Reader)
	if err != nil {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			Error:     fmt.Sprintf("error decompressing file: %v", err),
		}
	}

	// Calculate old file hash
	oldHash := calculateHash(oldData)

	// Call API to get file from new Cumulus
	apiURL := fmt.Sprintf("http://%s:%d/base/files/old/%d", apiHost, apiPort, mFile.FID)
	resp, err := client.Get(apiURL)
	if err != nil {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			OldHash:   oldHash,
			Error:     fmt.Sprintf("API error: %v", err),
		}
	}
	defer resp.Body.Close()

	// Check if file exists in new Cumulus
	if resp.StatusCode == http.StatusNotFound {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			OldHash:   oldHash,
			Error:     "file not found in new Cumulus",
		}
	}

	if resp.StatusCode != http.StatusOK {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			OldHash:   oldHash,
			Error:     fmt.Sprintf("API returned status %d", resp.StatusCode),
		}
	}

	// Read new file data
	newData, err := io.ReadAll(resp.Body)
	if err != nil {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "missing",
			OldHash:   oldHash,
			Error:     fmt.Sprintf("error reading API response: %v", err),
		}
	}

	// Calculate new file hash
	newHash := calculateHash(newData)

	// Compare hashes
	if oldHash != newHash {
		return &TestMismatch{
			CumulusID: mFile.FID,
			Filename:  mFile.Filename,
			Status:    "hash_mismatch",
			OldHash:   oldHash,
			NewHash:   newHash,
		}
	}

	// Files match
	return nil
}

// calculateHash computes SHA-256 hash of data
func calculateHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// saveMismatchesToJSON saves mismatches to a JSON file
func saveMismatchesToJSON(mismatches []TestMismatch, filename string) error {
	data, err := json.MarshalIndent(map[string]interface{}{
		"timestamp":        time.Now().Format(time.RFC3339),
		"total_mismatches": len(mismatches),
		"mismatches":       mismatches,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling JSON: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("error writing file: %w", err)
	}

	return nil
}
