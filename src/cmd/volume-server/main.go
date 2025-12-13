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
		"DB_PATH",
		"DATA_DIR",
		"DATA_FILE_SIZE",
		"MAX_UPLOAD_FILE_SIZE",
		"SERVER_PORT",
		"SERVER_ADDRESS",
		"USE_COMPRESS",
		"MINIMAL_COMPRESSION",
		"SWAGGER_HOST",
		"LOG_LEVEL",
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

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/database/cumulus3.db"
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

	// 1. Inicializace složky pro data
	dbDir := filepath.Dir(dbPath)
	os.MkdirAll(dbDir, 0755)

	// 2. Start Metadata DB (SQLite)
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=NORMAL", dbPath)
	metaStore, err := storage.NewMetadataSQL(dsn)
	if err != nil {
		panic("Nelze otevřít DB: " + err.Error())
	}
	// Důležité: Zavřít DB při ukončení programu
	defer metaStore.Close()

	// 3. Inicializace File Storage (zatím to naše jednoduché)
	fileStore := storage.NewStore(dataDir, maxDataFileSize)

	// 3b. Inicializace Metadata Loggeru (pro disaster recovery)
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

// { "fileID": "cbbfc3d7-6618-4df4-b734-86ddde3df0ba"} - XLS FILE
// { "fileID": "08899cef-8858-4d6a-85c1-1b5757d86d34" } - PDF FILE
// { "fileID": "2733d5fe-5810-44d6-9674-bd6eb30c5a38" } - PDF FILE
// { "fileID": "2497c116-bfb5-49fc-8bb2-f0fefa970a5d" } - IMAGE FILE JPG
// { "fileID": "06305333-940d-43a3-a7c7-97198d55d257" } - BINARY FILE - TEMP
// { "fileID": "03404f16-0faa-49f0-ba09-b12426d93710" } - KESS3 FILE
// { "fileID": "3447c37d-9eee-454c-a239-200c532fad07" } - KESS3 DEC FILE
