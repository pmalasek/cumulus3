package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/pmalasek/cumulus3/docs"
	"github.com/pmalasek/cumulus3/src/internal/api"
	"github.com/pmalasek/cumulus3/src/internal/service"
	"github.com/pmalasek/cumulus3/src/internal/storage"
	"github.com/pmalasek/cumulus3/src/internal/utils"
)

// @title Cumulus3
// @version 3.0.1
// @description High-performance distributed object storage server in Go (SeaweedFS architecture). Features smart deduplication (BLAKE2b), adaptive Zstd compression, SQLite (WAL) metadata, and native Prometheus metrics.

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @tag.name 01 - Base (internal)
// @tag.description Internal endpoints for backward compatibility with old Cumulus ID system

// @tag.name 02 - Files
// @tag.description Main file operations - upload, download, metadata, delete

// @tag.name 03 - Images
// @tag.description Image processing endpoints - thumbnails, previews, transformations

// @tag.name 04 - System
// @tag.description System endpoints - health checks, metrics

// @BasePath /

// printStartupConfiguration prints all configuration parameters at startup
func printStartupConfiguration() {
	utils.Info("CONFIG", "=== Startup Configuration ===")

	// Helper function to mask passwords
	maskIfPassword := func(key, value string) string {
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "password") ||
			strings.Contains(lowerKey, "passwd") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "key") && !strings.Contains(lowerKey, "key_path") {
			if value != "" {
				return "********"
			}
			return ""
		}
		return value
	}

	// Define configuration parameters to display
	configParams := []string{
		"DATABASE_TYPE",
		"DB_SQLITE_PATH",
		"PG_DATABASE_URL",
		"DATA_DIR",
		"DATA_FILE_SIZE",
		"MAX_UPLOAD_FILE_SIZE",
		"SERVER_PORT",
		"SERVER_ADDRESS",
		"USE_COMPRESS",
		"MINIMAL_COMPRESSION",
		"SWAGGER_HOST",
		"LOG_LEVEL",
		"CLEANUP_INTERVAL",
		"PENDING_BLOB_CLEANUP_INTERVAL",
		"PENDING_BLOB_MAX_AGE",
	}

	for _, param := range configParams {
		value := os.Getenv(param)
		displayValue := maskIfPassword(param, value)
		if value == "" {
			displayValue = "(not set)"
		}
		utils.Info("CONFIG", "  %s = %s", param, displayValue)
	}

	utils.Info("CONFIG", "=============================")
}

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

	// Initialize centralized logger
	utils.InitLogger()

	// Print all configuration parameters
	printStartupConfiguration()

	utils.Info("STARTUP", "Cumulus3 starting up, log level: %s", utils.GetLogLevel())

	// Database configuration
	dbType := os.Getenv("DATABASE_TYPE")
	if dbType == "" {
		dbType = "sqlite" // Default to SQLite for backward compatibility
	}

	var dsn string
	switch dbType {
	case "sqlite":
		dbPath := os.Getenv("DB_SQLITE_PATH")
		if dbPath == "" {
			dbPath = "./data/database/cumulus3.db"
		}
		// Create database directory
		dbDir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			panic("Nelze vytvořit adresář pro DB: " + err.Error())
		}
		dsn = fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=NORMAL", dbPath)
		utils.Info("DATABASE", "Using SQLite database: %s", dbPath)

	case "postgresql":
		pgURL := os.Getenv("PG_DATABASE_URL")
		if pgURL == "" {
			panic("PG_DATABASE_URL is required when DATABASE_TYPE=postgresql")
		}
		dsn = pgURL
		utils.Info("DATABASE", "Using PostgreSQL database")

	default:
		panic(fmt.Sprintf("Unsupported DATABASE_TYPE: %s (use 'sqlite' or 'postgresql')", dbType))
	}

	dataFileSizeStr := os.Getenv("DATA_FILE_SIZE")
	var maxDataFileSize int64 = 10 << 20 // Default 10MB for data file
	if dataFileSizeStr != "" {
		if s, err := utils.ParseBytes(dataFileSizeStr); err == nil {
			maxDataFileSize = s
		} else {
			utils.Warn("CONFIG", "Invalid DATA_FILE_SIZE format: %v, using default", err)
		}
	}

	maxUploadFileSizeStr := os.Getenv("MAX_UPLOAD_FILE_SIZE")
	var maxUploadSize int64 = 50 << 20 // Default 50MB for upload
	if maxUploadFileSizeStr != "" {
		if s, err := utils.ParseBytes(maxUploadFileSizeStr); err == nil {
			maxUploadSize = s
		} else {
			utils.Warn("CONFIG", "Invalid MAX_UPLOAD_FILE_SIZE format: %v, using default", err)
		}
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}

	// Start Metadata DB
	metaStore, err := storage.NewMetadataSQL(dbType, dsn)
	if err != nil {
		panic("Nelze otevřít DB: " + err.Error())
	}
	// Důležité: Zavřít DB při ukončení programu
	defer metaStore.Close()

	// Inicializace File Storage
	fileStore := storage.NewStore(dataDir, maxDataFileSize)

	// Inicializace Metadata Loggeru (pro disaster recovery)
	metaLogger := storage.NewMetadataLogger(dataDir)

	// Start metrics updater
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			total, deleted, err := metaStore.GetStorageStats()
			if err != nil {
				utils.Error("METRICS", "Error getting storage stats: %v", err)
				continue
			}
			api.UpdateStorageMetrics(total, deleted)
		}
	}()

	// Start expired temporary files cleanup
	cleanupIntervalStr := os.Getenv("CLEANUP_INTERVAL")
	if cleanupIntervalStr == "" {
		cleanupIntervalStr = "1h" // Default: 1 hour
	}
	cleanupInterval, err := time.ParseDuration(cleanupIntervalStr)
	if err != nil {
		utils.Warn("CONFIG", "Invalid CLEANUP_INTERVAL format '%s': %v, using default 1h", cleanupIntervalStr, err)
		cleanupInterval = 1 * time.Hour
	}

	// Start stale pending blob cleanup
	pendingCleanupIntervalStr := os.Getenv("PENDING_BLOB_CLEANUP_INTERVAL")
	if pendingCleanupIntervalStr == "" {
		pendingCleanupIntervalStr = cleanupIntervalStr // default: same cadence as temp-file cleanup
	}
	pendingCleanupInterval, err := time.ParseDuration(pendingCleanupIntervalStr)
	if err != nil {
		utils.Warn("CONFIG", "Invalid PENDING_BLOB_CLEANUP_INTERVAL format '%s': %v, using cleanup interval %v", pendingCleanupIntervalStr, err, cleanupInterval)
		pendingCleanupInterval = cleanupInterval
	}

	pendingBlobMaxAgeStr := os.Getenv("PENDING_BLOB_MAX_AGE")
	if pendingBlobMaxAgeStr == "" {
		pendingBlobMaxAgeStr = "30m"
	}
	pendingBlobMaxAge, err := time.ParseDuration(pendingBlobMaxAgeStr)
	if err != nil {
		utils.Warn("CONFIG", "Invalid PENDING_BLOB_MAX_AGE format '%s': %v, using default 30m", pendingBlobMaxAgeStr, err)
		pendingBlobMaxAge = 30 * time.Minute
	}

	go func() {
		// Delay first run to avoid startup overhead
		time.Sleep(2 * time.Minute)

		ticker := time.NewTicker(pendingCleanupInterval)
		defer ticker.Stop()

		utils.Info("CLEANUP", "Stale pending blob cleanup scheduled every %v (max age: %v)", pendingCleanupInterval, pendingBlobMaxAge)

		for {
			utils.Info("CLEANUP", "Starting cleanup of stale pending blobs")
			deletedCount, totalStale, err := metaStore.CleanupStalePendingBlobs(pendingBlobMaxAge)
			if err != nil {
				utils.Error("CLEANUP", "Error cleaning up stale pending blobs: %v", err)
			} else if totalStale == 0 {
				utils.Info("CLEANUP", "No stale pending blobs found")
			} else if deletedCount == totalStale {
				utils.Info("CLEANUP", "Successfully cleaned up %d stale pending blob(s)", deletedCount)
			} else if deletedCount > 0 {
				utils.Warn("CLEANUP", "Cleaned up %d of %d stale pending blobs (%d failed)", deletedCount, totalStale, totalStale-deletedCount)
			} else {
				utils.Error("CLEANUP", "Found %d stale pending blobs but all deletions failed", totalStale)
			}

			<-ticker.C
		}
	}()

	go func() {
		// Run first cleanup after 1 minute to avoid startup overhead
		time.Sleep(1 * time.Minute)

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		utils.Info("CLEANUP", "Expired temporary files cleanup scheduled every %v", cleanupInterval)

		// Run cleanup immediately on first iteration
		for {
			utils.Info("CLEANUP", "Starting cleanup of expired temporary files")
			deletedCount, totalExpired, _, err := metaStore.CleanupExpiredTemporaryFiles()
			if err != nil {
				utils.Error("CLEANUP", "Error cleaning up expired files: %v", err)
			} else if totalExpired == 0 {
				utils.Info("CLEANUP", "No expired temporary files found")
			} else if deletedCount == totalExpired {
				utils.Info("CLEANUP", "Successfully cleaned up %d expired temporary file(s)", deletedCount)
			} else if deletedCount > 0 {
				utils.Warn("CLEANUP", "Cleaned up %d of %d expired temporary files (%d failed)", deletedCount, totalExpired, totalExpired-deletedCount)
			} else {
				utils.Error("CLEANUP", "Found %d expired files but all deletions failed", totalExpired)
			}

			<-ticker.C
		}
	}()

	// 4. Inicializace API serveru (teď už mu budeme posílat i metaStore!)
	// Pozor: Zde musíme upravit strukturu Server v api/handlers.go (viz další krok)
	compressionMode := os.Getenv("USE_COMPRESS")
	if compressionMode == "" {
		compressionMode = "Auto"
	}

	minCompressionRatio := 10.0
	if val := os.Getenv("MINIMAL_COMPRESSION"); val != "" {
		// Odstraníme případný znak % na konci
		val = strings.TrimSuffix(val, "%")
		if v, err := strconv.ParseFloat(val, 64); err == nil {
			minCompressionRatio = v
		} else {
			utils.Warn("CONFIG", "Invalid MINIMAL_COMPRESSION format: %v, using default 10%%", err)
		}
	}

	fileService := service.NewFileService(fileStore, metaStore, metaLogger, compressionMode, minCompressionRatio)

	srv := &api.Server{
		FileService:   fileService,
		MaxUploadSize: maxUploadSize,
	}

	// Nastavení Swagger host (můžete nastavit přes SWAGGER_HOST env)
	// Pokud není nastaveno, Swagger použije aktuální URL v prohlížeči
	swaggerHost := os.Getenv("SWAGGER_HOST")
	if swaggerHost != "" {
		docs.SwaggerInfo.Host = swaggerHost
	} else {
		// Necháme prázdné - Swagger použije URL ze kterého se načetl
		docs.SwaggerInfo.Host = ""
	}

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8800"
	}

	handler := srv.Routes()

	serverAddr := os.Getenv("SERVER_ADDRESS") + ":" + port
	utils.Info("STARTUP", "🚀 Server listening on %s", serverAddr)
	http.ListenAndServe(serverAddr, handler)
}
