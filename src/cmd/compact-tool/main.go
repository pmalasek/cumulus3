package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pmalasek/cumulus3/src/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Load .env if exists
	godotenv.Load()

	command := os.Args[1]

	switch command {
	case "volumes":
		handleVolumesCommand()
	case "db":
		handleDBCommand()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Cumulus3 Compact Tool - Database and Volume Maintenance")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  compact-tool volumes list                    - List all volumes and their fragmentation")
	fmt.Println("  compact-tool volumes compact <id>            - Compact specific volume by ID")
	fmt.Println("  compact-tool volumes compact-all [--threshold 20] - Compact all volumes with fragmentation >= threshold%")
	fmt.Println("  compact-tool db vacuum                       - Perform SQLite VACUUM (requires downtime)")
	fmt.Println("  compact-tool help                            - Show this help")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  DB_PATH   - Path to SQLite database (default: ./data/database/cumulus3.db)")
	fmt.Println("  DATA_DIR  - Path to volume directory (default: ./data/volumes)")
	fmt.Println()
	fmt.Println("Notes:")
	fmt.Println("  - Volume compaction can run while server is running (per-volume locking)")
	fmt.Println("  - Database VACUUM requires stopping the server (full database lock)")
	fmt.Println("  - Compaction requires free disk space equal to volume size")
}

func handleVolumesCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Error: volumes command requires subcommand (list, compact, compact-all)")
		os.Exit(1)
	}

	subcommand := os.Args[2]

	switch subcommand {
	case "list":
		listVolumes()
	case "compact":
		if len(os.Args) < 4 {
			fmt.Println("Error: compact requires volume ID")
			fmt.Println("Usage: compact-tool volumes compact <id>")
			os.Exit(1)
		}
		volumeID, err := strconv.ParseInt(os.Args[3], 10, 64)
		if err != nil {
			fmt.Printf("Error: invalid volume ID: %v\n", err)
			os.Exit(1)
		}
		compactVolume(volumeID)
	case "compact-all":
		flags := flag.NewFlagSet("compact-all", flag.ExitOnError)
		threshold := flags.Float64("threshold", 20.0, "Minimum fragmentation percentage to compact")
		flags.Parse(os.Args[3:])
		compactAllVolumes(*threshold)
	default:
		fmt.Printf("Unknown volumes subcommand: %s\n", subcommand)
		os.Exit(1)
	}
}

func handleDBCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Error: db command requires subcommand (vacuum)")
		os.Exit(1)
	}

	subcommand := os.Args[2]

	switch subcommand {
	case "vacuum":
		vacuumDatabase()
	default:
		fmt.Printf("Unknown db subcommand: %s\n", subcommand)
		os.Exit(1)
	}
}

func getConfig() (dbPath, dataDir string) {
	dbPath = os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/database/cumulus3.db"
	}

	dataDir = os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data/volumes"
	}

	return dbPath, dataDir
}

func listVolumes() {
	dbPath, dataDir := getConfig()

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		fmt.Printf("Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	metaStore, err := storage.NewMetadataSQL(dbPath)
	if err != nil {
		fmt.Printf("Error opening metadata store: %v\n", err)
		os.Exit(1)
	}
	defer metaStore.Close()

	volumes, err := metaStore.GetVolumesToCompact(0) // Get all volumes
	if err != nil {
		fmt.Printf("Error getting volumes: %v\n", err)
		os.Exit(1)
	}

	if len(volumes) == 0 {
		fmt.Println("No volumes found.")
		return
	}

	fmt.Println("Volume Status:")
	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	fmt.Printf("%-8s %-15s %-15s %-15s %-12s %-8s\n", "ID", "Total Size", "Deleted Size", "Used Size", "Fragmentation", "Status")
	fmt.Println("─────────────────────────────────────────────────────────────────────────")

	for _, vol := range volumes {
		fragmentation := 0.0
		if vol.SizeTotal > 0 {
			fragmentation = (float64(vol.SizeDeleted) / float64(vol.SizeTotal)) * 100
		}

		totalStr := formatBytes(vol.SizeTotal)
		deletedStr := formatBytes(vol.SizeDeleted)
		usedStr := formatBytes(vol.SizeTotal - vol.SizeDeleted)
		fragStr := fmt.Sprintf("%.1f%%", fragmentation)

		// Check if file exists
		status := "OK"
		volumePath := filepath.Join(dataDir, fmt.Sprintf("volume_%08d.dat", vol.ID))
		if _, err := os.Stat(volumePath); os.IsNotExist(err) {
			// Try legacy format
			volumePath = filepath.Join(dataDir, fmt.Sprintf("volume_%d.dat", vol.ID))
			if _, err := os.Stat(volumePath); os.IsNotExist(err) {
				status = "MISSING"
			}
		}

		fmt.Printf("%-8d %-15s %-15s %-15s %-12s %-8s\n",
			vol.ID, totalStr, deletedStr, usedStr, fragStr, status)
	}

	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	fmt.Println()
	fmt.Println("Tip: Run 'compact-tool volumes compact-all --threshold 20' to compact volumes with >20% fragmentation")
}

func compactVolume(volumeID int64) {
	dbPath, dataDir := getConfig()

	fmt.Printf("Starting compaction of volume %d...\n", volumeID)

	store := storage.NewStore(dataDir, 100*1024*1024) // Size doesn't matter for compaction

	metaStore, err := storage.NewMetadataSQL(dbPath)
	if err != nil {
		fmt.Printf("Error opening metadata store: %v\n", err)
		os.Exit(1)
	}
	defer metaStore.Close()

	// Get volume info before compaction
	volumes, err := metaStore.GetVolumesToCompact(0)
	if err != nil {
		fmt.Printf("Error getting volume info: %v\n", err)
		os.Exit(1)
	}

	var beforeVol *storage.VolumeInfo
	for _, vol := range volumes {
		if int64(vol.ID) == volumeID {
			beforeVol = &vol
			break
		}
	}

	if beforeVol == nil {
		fmt.Printf("Volume %d not found in database\n", volumeID)
		os.Exit(1)
	}

	beforeFrag := 0.0
	if beforeVol.SizeTotal > 0 {
		beforeFrag = (float64(beforeVol.SizeDeleted) / float64(beforeVol.SizeTotal)) * 100
	}

	fmt.Printf("Before: Total=%s, Deleted=%s, Fragmentation=%.1f%%\n",
		formatBytes(beforeVol.SizeTotal),
		formatBytes(beforeVol.SizeDeleted),
		beforeFrag)

	// Perform compaction
	err = store.CompactVolume(volumeID, metaStore)
	if err != nil {
		fmt.Printf("Error during compaction: %v\n", err)
		os.Exit(1)
	}

	// Get volume info after compaction
	volumes, err = metaStore.GetVolumesToCompact(0)
	if err != nil {
		fmt.Printf("Warning: Could not get updated volume info: %v\n", err)
	} else {
		for _, vol := range volumes {
			if int64(vol.ID) == volumeID {
				afterFrag := 0.0
				if vol.SizeTotal > 0 {
					afterFrag = (float64(vol.SizeDeleted) / float64(vol.SizeTotal)) * 100
				}

				savedSpace := beforeVol.SizeTotal - vol.SizeTotal

				fmt.Printf("After:  Total=%s, Deleted=%s, Fragmentation=%.1f%%\n",
					formatBytes(vol.SizeTotal),
					formatBytes(vol.SizeDeleted),
					afterFrag)
				fmt.Printf("✓ Space saved: %s\n", formatBytes(savedSpace))
				break
			}
		}
	}

	fmt.Println("✓ Compaction completed successfully")
}

func compactAllVolumes(threshold float64) {
	dbPath, dataDir := getConfig()

	store := storage.NewStore(dataDir, 100*1024*1024)

	metaStore, err := storage.NewMetadataSQL(dbPath)
	if err != nil {
		fmt.Printf("Error opening metadata store: %v\n", err)
		os.Exit(1)
	}
	defer metaStore.Close()

	volumes, err := metaStore.GetVolumesToCompact(threshold)
	if err != nil {
		fmt.Printf("Error getting volumes to compact: %v\n", err)
		os.Exit(1)
	}

	if len(volumes) == 0 {
		fmt.Printf("No volumes found with fragmentation >= %.1f%%\n", threshold)
		return
	}

	fmt.Printf("Found %d volume(s) with fragmentation >= %.1f%%\n\n", len(volumes), threshold)

	totalSaved := int64(0)
	successCount := 0
	failCount := 0

	for i, vol := range volumes {
		fragmentation := 0.0
		if vol.SizeTotal > 0 {
			fragmentation = (float64(vol.SizeDeleted) / float64(vol.SizeTotal)) * 100
		}

		fmt.Printf("[%d/%d] Compacting volume %d (fragmentation: %.1f%%)...\n",
			i+1, len(volumes), vol.ID, fragmentation)

		beforeSize := vol.SizeTotal

		err = store.CompactVolume(int64(vol.ID), metaStore)
		if err != nil {
			fmt.Printf("  ✗ Error: %v\n\n", err)
			failCount++
			continue
		}

		// Get updated info
		volumes2, _ := metaStore.GetVolumesToCompact(0)
		for _, v := range volumes2 {
			if v.ID == vol.ID {
				saved := beforeSize - v.SizeTotal
				totalSaved += saved
				fmt.Printf("  ✓ Saved: %s\n\n", formatBytes(saved))
				break
			}
		}

		successCount++
	}

	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	fmt.Printf("Summary: %d succeeded, %d failed\n", successCount, failCount)
	fmt.Printf("Total space saved: %s\n", formatBytes(totalSaved))
	fmt.Println("─────────────────────────────────────────────────────────────────────────")
}

func vacuumDatabase() {
	dbPath, _ := getConfig()

	fmt.Println("⚠️  WARNING: Database VACUUM requires exclusive access!")
	fmt.Println("⚠️  Please ensure the Cumulus3 server is stopped before proceeding.")
	fmt.Println()
	fmt.Print("Continue? (yes/no): ")

	var response string
	fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))

	if response != "yes" && response != "y" {
		fmt.Println("Cancelled.")
		return
	}

	fmt.Println()
	fmt.Println("Opening database...")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		fmt.Printf("Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Get database size before VACUUM
	var pageSizeBefore, pageCountBefore int64
	db.QueryRow("PRAGMA page_size").Scan(&pageSizeBefore)
	db.QueryRow("PRAGMA page_count").Scan(&pageCountBefore)
	sizeBefore := pageSizeBefore * pageCountBefore

	fmt.Printf("Database size before VACUUM: %s\n", formatBytes(sizeBefore))
	fmt.Println("Starting VACUUM (this may take several minutes)...")

	_, err = db.Exec("VACUUM")
	if err != nil {
		fmt.Printf("Error during VACUUM: %v\n", err)
		os.Exit(1)
	}

	// Get database size after VACUUM
	var pageSizeAfter, pageCountAfter int64
	db.QueryRow("PRAGMA page_size").Scan(&pageSizeAfter)
	db.QueryRow("PRAGMA page_count").Scan(&pageCountAfter)
	sizeAfter := pageSizeAfter * pageCountAfter

	savedSpace := sizeBefore - sizeAfter

	fmt.Println()
	fmt.Println("✓ VACUUM completed successfully")
	fmt.Printf("Database size after VACUUM: %s\n", formatBytes(sizeAfter))
	fmt.Printf("Space saved: %s (%.1f%%)\n",
		formatBytes(savedSpace),
		(float64(savedSpace)/float64(sizeBefore))*100)
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
