package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/pmalasek/cumulus3/docs"
	"github.com/pmalasek/cumulus3/src/internal/api"
	"github.com/pmalasek/cumulus3/src/internal/storage"
	"github.com/pmalasek/cumulus3/src/internal/utils"
)

// @title Cumulus3 API
// @version 1.0
// @description This is a sample server for Cumulus3 object storage.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:8080
// @BasePath /
func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

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
			log.Printf("Invalid DATA_FILE_SIZE format: %v, using default", err)
		}
	}

	maxUploadFileSizeStr := os.Getenv("MAX_UPLOAD_FILE_SIZE")
	var maxUploadSize int64 = 50 << 20 // Default 50MB for upload
	if maxUploadFileSizeStr != "" {
		if s, err := utils.ParseBytes(maxUploadFileSizeStr); err == nil {
			maxUploadSize = s
		} else {
			log.Printf("Invalid MAX_UPLOAD_FILE_SIZE format: %v, using default", err)
		}
	}

	// 1. Inicializace složky pro data
	os.MkdirAll("./data/metadata", 0755)

	// 2. Start Metadata DB (SQLite)
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=NORMAL", dbPath)
	metaStore, err := storage.NewMetadataSQL(dsn)
	if err != nil {
		panic("Nelze otevřít DB: " + err.Error())
	}
	// Důležité: Zavřít DB při ukončení programu
	defer metaStore.Close()

	// 3. Inicializace File Storage (zatím to naše jednoduché)
	fileStore := storage.NewStore("./data", maxDataFileSize)

	// 4. Inicializace API serveru (teď už mu budeme posílat i metaStore!)
	// Pozor: Zde musíme upravit strukturu Server v api/handlers.go (viz další krok)
	srv := &api.Server{
		Store:         fileStore,
		MetaStore:     metaStore,
		MaxUploadSize: maxUploadSize,
	}

	// Nastavení dynamické IP pro Swagger
	myIP := utils.GetOutboundIP()
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}
	docs.SwaggerInfo.Host = fmt.Sprintf("%s:%s", myIP, port)

	handler := srv.Routes()

	fmt.Println("🚀 Běžíme na " + os.Getenv("SERVER_ADDRESS") + ":" + port)
	http.ListenAndServe(os.Getenv("SERVER_ADDRESS")+":"+port, handler)
}
