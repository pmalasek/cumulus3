package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pmalasek/cumulus3/src/internal/storage"
	"github.com/pmalasek/cumulus3/src/internal/utils"
)

type BlobInfo struct {
	ID             int64
	VolumeID       int64
	Offset         int64
	SizeCompressed int64
	CompAlg        uint8
	Hash           string
}

type FileInfo struct {
	ID           string
	Name         string
	BlobID       int64
	OldCumulusID *int64
	ExpiresAt    *int64
	CreatedAt    int64
	Tags         string
}

func main() {
	dataDir := flag.String("data-dir", "./data/volumes", "Path to data directory with volume files")
	dbPath := flag.String("db-path", "./data/database/cumulus3_rebuilt.db", "Path to output database file")
	flag.Parse()

	fmt.Println("🔨 Cumulus3 Database Rebuild Tool")
	fmt.Println("===================================")
	fmt.Printf("Data directory: %s\n", *dataDir)
	fmt.Printf("Output database: %s\n\n", *dbPath)

	// Remove existing database if it exists
	if _, err := os.Stat(*dbPath); err == nil {
		fmt.Printf("⚠️  Removing existing database: %s\n", *dbPath)
		if err := os.Remove(*dbPath); err != nil {
			log.Fatalf("Failed to remove existing database: %v", err)
		}
	}

	// Initialize new database
	fmt.Println("📊 Initializing database schema...")
	meta, err := storage.NewMetadataSQL(*dbPath)
	if err != nil {
		log.Fatalf("Failed to create database: %v", err)
	}
	defer meta.Close()

	// Scan volumes
	fmt.Println("\n🔍 Scanning volume files...")
	blobs, volumeSizes, err := scanVolumes(*dataDir)
	if err != nil {
		log.Fatalf("Failed to scan volumes: %v", err)
	}
	fmt.Printf("✅ Found %d blobs in %d volumes\n", len(blobs), len(volumeSizes))

	// Read files metadata
	fmt.Println("\n📂 Reading files metadata...")
	files, err := readFilesMetadata(filepath.Join(filepath.Dir(*dataDir), "database", "files_metadata.bin"))
	if err != nil {
		files, err = readFilesMetadata(filepath.Join(*dataDir, "files_metadata.bin"))
		if err != nil {
			log.Printf("⚠️  Warning: Failed to read files_metadata.bin: %v", err)
			files = []FileInfo{}
		}
	}
	fmt.Printf("✅ Found %d file records\n", len(files))

	// Populate database
	fmt.Println("\n💾 Populating database...")

	// Insert blobs
	fmt.Println("  → Inserting blobs...")
	blobCount := 0
	for _, blob := range blobs {
		mimeType, category, subtype := detectBlobType(*dataDir, blob)

		fileTypeID, err := meta.GetOrCreateFileType(mimeType, category, subtype)
		if err != nil {
			log.Printf("Warning: Failed to create file type for blob %d: %v", blob.ID, err)
			continue
		}

		err = meta.CreateBlobWithID(blob.ID, blob.Hash)
		if err != nil {
			log.Printf("Warning: Failed to create blob %d: %v", blob.ID, err)
			continue
		}

		compAlg := "none"
		if blob.CompAlg == 1 {
			compAlg = "gzip"
		} else if blob.CompAlg == 2 {
			compAlg = "zstd"
		}

		err = meta.UpdateBlobLocation(blob.ID, blob.VolumeID, blob.Offset, 0, blob.SizeCompressed, compAlg, fileTypeID)
		if err != nil {
			log.Printf("Warning: Failed to update blob location %d: %v", blob.ID, err)
			continue
		}

		blobCount++
		if blobCount%100 == 0 {
			fmt.Printf("    Progress: %d/%d blobs\r", blobCount, len(blobs))
		}
	}
	fmt.Printf("  ✅ Inserted %d blobs                    \n", blobCount)

	// Insert files
	fmt.Println("  → Inserting files...")
	fileCount := 0
	for _, file := range files {
		var expiresAt *time.Time
		if file.ExpiresAt != nil {
			t := time.Unix(*file.ExpiresAt, 0)
			expiresAt = &t
		}

		err := meta.SaveFile(storage.File{
			ID:           file.ID,
			Name:         file.Name,
			BlobID:       file.BlobID,
			OldCumulusID: file.OldCumulusID,
			ExpiresAt:    expiresAt,
			CreatedAt:    time.Unix(file.CreatedAt, 0),
			Tags:         file.Tags,
		})
		if err != nil {
			log.Printf("Warning: Failed to save file %s: %v", file.ID, err)
			continue
		}

		fileCount++
		if fileCount%100 == 0 {
			fmt.Printf("    Progress: %d/%d files\r", fileCount, len(files))
		}
	}
	fmt.Printf("  ✅ Inserted %d files                    \n", fileCount)

	// Update volumes table
	fmt.Println("  → Updating volume sizes...")
	for volumeID, size := range volumeSizes {
		_, err := meta.GetDB().Exec(`
			INSERT INTO volumes (id, size_total, size_deleted) VALUES (?, ?, 0)
			ON CONFLICT(id) DO UPDATE SET size_total = ?
		`, volumeID, size, size)
		if err != nil {
			log.Printf("Warning: Failed to update volume %d: %v", volumeID, err)
		}
	}
	fmt.Printf("  ✅ Updated %d volumes\n", len(volumeSizes))

	fmt.Println("\n🎉 Database rebuild complete!")
	fmt.Printf("   Database: %s\n", *dbPath)
	fmt.Printf("   Blobs: %d\n", blobCount)
	fmt.Printf("   Files: %d\n", fileCount)
	fmt.Printf("   Volumes: %d\n", len(volumeSizes))
}

func scanVolumes(dir string) ([]BlobInfo, map[int64]int64, error) {
	blobs := []BlobInfo{}
	volumeSizes := make(map[int64]int64)

	files, err := filepath.Glob(filepath.Join(dir, "volume_*.dat"))
	if err != nil {
		return nil, nil, err
	}

	for _, file := range files {
		var volumeID int64
		baseName := filepath.Base(file)
		if strings.HasPrefix(baseName, "volume_") {
			fmt.Sscanf(baseName, "volume_%d.dat", &volumeID)
		}

		metaName := baseName[:len(baseName)-4] + ".meta"
		metaPath := filepath.Join(dir, metaName)

		if _, err := os.Stat(metaPath); err == nil {
			fmt.Printf("  → Reading %s (using .meta)\n", baseName)
			volumeBlobs, err := readMetaFile(metaPath, volumeID)
			if err == nil {
				blobs = append(blobs, volumeBlobs...)
				totalSize := int64(0)
				for _, blob := range volumeBlobs {
					totalSize += int64(storage.HeaderSize) + blob.SizeCompressed + int64(storage.FooterSize)
				}
				volumeSizes[volumeID] = totalSize
				continue
			}
			log.Printf("    Warning: Failed to read .meta: %v", err)
		}

		fmt.Printf("  → Reading %s (scanning .dat)\n", baseName)
		volumeBlobs, err := scanDatFile(file, volumeID)
		if err != nil {
			log.Printf("    Warning: Failed to scan %s: %v", baseName, err)
			continue
		}
		blobs = append(blobs, volumeBlobs...)

		totalSize := int64(0)
		for _, blob := range volumeBlobs {
			totalSize += int64(storage.HeaderSize) + blob.SizeCompressed + int64(storage.FooterSize)
		}
		volumeSizes[volumeID] = totalSize
	}

	return blobs, volumeSizes, nil
}

func readMetaFile(metaPath string, volumeID int64) ([]BlobInfo, error) {
	f, err := os.Open(metaPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	blobs := []BlobInfo{}
	recordSize := 29
	buf := make([]byte, recordSize)

	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		blobID := int64(binary.BigEndian.Uint64(buf[0:8]))
		offset := int64(binary.BigEndian.Uint64(buf[8:16]))
		size := int64(binary.BigEndian.Uint64(buf[16:24]))
		compAlg := buf[24]

		hash := fmt.Sprintf("blob_%d", blobID)

		blobs = append(blobs, BlobInfo{
			ID:             blobID,
			VolumeID:       volumeID,
			Offset:         offset,
			SizeCompressed: size,
			CompAlg:        compAlg,
			Hash:           hash,
		})
	}

	return blobs, nil
}

func scanDatFile(file string, volumeID int64) ([]BlobInfo, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	blobs := []BlobInfo{}
	header := make([]byte, storage.HeaderSize)

	for {
		offset, _ := f.Seek(0, io.SeekCurrent)

		if _, err := io.ReadFull(f, header); err != nil {
			if err == io.EOF {
				break
			}
			return blobs, nil
		}

		magic := binary.BigEndian.Uint32(header[0:4])
		if magic != uint32(storage.MagicBytes) {
			break
		}

		compAlg := header[5]
		size := int64(binary.BigEndian.Uint64(header[6:14]))
		blobID := int64(binary.BigEndian.Uint64(header[14:22]))

		hash := fmt.Sprintf("blob_%d", blobID)

		blobs = append(blobs, BlobInfo{
			ID:             blobID,
			VolumeID:       volumeID,
			Offset:         offset,
			SizeCompressed: size,
			CompAlg:        compAlg,
			Hash:           hash,
		})

		if _, err := f.Seek(size+int64(storage.FooterSize), io.SeekCurrent); err != nil {
			break
		}
	}

	return blobs, nil
}

func readFilesMetadata(path string) ([]FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	files := []FileInfo{}

	for {
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(f, lenBuf); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		recordLen := binary.BigEndian.Uint32(lenBuf)

		record := make([]byte, recordLen)
		if _, err := io.ReadFull(f, record); err != nil {
			return nil, err
		}

		cursor := 0

		idLen := binary.BigEndian.Uint16(record[cursor : cursor+2])
		cursor += 2

		id := string(record[cursor : cursor+int(idLen)])
		cursor += int(idLen)

		blobID := int64(binary.BigEndian.Uint64(record[cursor : cursor+8]))
		cursor += 8

		createdAt := int64(binary.BigEndian.Uint64(record[cursor : cursor+8]))
		cursor += 8

		flags := record[cursor]
		cursor += 1

		var oldCumulusID *int64
		var expiresAt *int64
		var tags string

		if flags&(1<<0) != 0 {
			val := int64(binary.BigEndian.Uint64(record[cursor : cursor+8]))
			oldCumulusID = &val
			cursor += 8
		}
		if flags&(1<<1) != 0 {
			val := int64(binary.BigEndian.Uint64(record[cursor : cursor+8]))
			expiresAt = &val
			cursor += 8
		}
		if flags&(1<<2) != 0 {
			tagsLen := binary.BigEndian.Uint16(record[cursor : cursor+2])
			cursor += 2
			tags = string(record[cursor : cursor+int(tagsLen)])
			cursor += int(tagsLen)
		}

		nameLen := binary.BigEndian.Uint16(record[cursor : cursor+2])
		cursor += 2

		name := string(record[cursor : cursor+int(nameLen)])

		files = append(files, FileInfo{
			ID:           id,
			Name:         name,
			BlobID:       blobID,
			OldCumulusID: oldCumulusID,
			ExpiresAt:    expiresAt,
			CreatedAt:    createdAt,
			Tags:         tags,
		})
	}

	return files, nil
}

func detectBlobType(dataDir string, blob BlobInfo) (string, string, string) {
	volumePath := filepath.Join(dataDir, fmt.Sprintf("volume_%08d.dat", blob.VolumeID))
	f, err := os.Open(volumePath)
	if err != nil {
		return "application/octet-stream", "binary", ""
	}
	defer f.Close()

	if _, err := f.Seek(blob.Offset+int64(storage.HeaderSize), io.SeekStart); err != nil {
		return "application/octet-stream", "binary", ""
	}

	sampleSize := int64(512)
	if blob.SizeCompressed < sampleSize {
		sampleSize = blob.SizeCompressed
	}
	sample := make([]byte, sampleSize)
	if _, err := io.ReadFull(f, sample); err != nil {
		return "application/octet-stream", "binary", ""
	}

	// For now, just detect from raw/compressed data
	// Full decompression would be too slow for rebuild
	result := utils.DetectFileType(sample)
	return result.ContentType, result.Type, result.Subtype
}
