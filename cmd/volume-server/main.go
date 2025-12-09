package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/pmalasek/cumulus3/internal/api"
	"github.com/pmalasek/cumulus3/internal/storage"
	"github.com/pmalasek/cumulus3/internal/utils"
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
		dbPath = "./data/metadata/metadata.db"
	}

	dataFileSizeStr := os.Getenv("DATA_FILE_SIZE")
	var maxUploadSize int64 = 10 << 20 // Default 10MB
	if dataFileSizeStr != "" {
		if s, err := utils.ParseBytes(dataFileSizeStr); err == nil {
			maxUploadSize = s
		} else {
			log.Printf("Invalid DATA_FILE_SIZE format: %v, using default", err)
		}
	}

	// 1. Inicializace složky pro data
	os.MkdirAll("./data/metadata", 0755)

	// 2. Start Metadata DB (SQLite)
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_sync=NORMAL", dbPath)
	metaStore, err := storage.NewDatabaseSQL(dsn)
	if err != nil {
		panic("Nelze otevřít DB: " + err.Error())
	}
	// Důležité: Zavřít DB při ukončení programu
	defer metaStore.Close()

	// 3. Inicializace File Storage (zatím to naše jednoduché)
	fileStore := storage.NewStore("./data")

	// 4. Inicializace API serveru (teď už mu budeme posílat i metaStore!)
	// Pozor: Zde musíme upravit strukturu Server v api/handlers.go (viz další krok)
	srv := &api.Server{
		Store:         fileStore,
		MetaStore:     metaStore,
		MaxUploadSize: maxUploadSize,
	}

	handler := srv.Routes()

	fmt.Println("🚀 Běžíme na :", os.Getenv("SERVER_PORT"))
	http.ListenAndServe(":"+os.Getenv("SERVER_PORT"), handler)
}
