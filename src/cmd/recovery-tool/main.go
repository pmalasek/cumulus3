package main

import (
	"compress/gzip"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
	"github.com/pmalasek/cumulus3/src/internal/storage"
)

// BlobLocation drží informaci, kde najít data pro dané BlobID
type BlobLocation struct {
	VolumePath     string
	Offset         int64
	SizeCompressed int64
	CompAlg        uint8
}

func main() {
	dataPath := flag.String("src", "./data", "Cesta ke zdrojovým datům (kde jsou volume_*.dat a files.bin)")
	restorePath := flag.String("dst", "./restored", "Cesta, kam se mají obnovit soubory")
	flag.Parse()

	if *dataPath == "" || *restorePath == "" {
		flag.Usage()
		os.Exit(1)
	}

	fmt.Println("🔍 Začínám analýzu volume souborů...")
	blobMap, err := scanVolumes(*dataPath)
	if err != nil {
		log.Fatalf("Chyba při skenování volumes: %v", err)
	}
	fmt.Printf("✅ Nalezeno %d unikátních blobů.\n", len(blobMap))

	fmt.Println("📂 Začínám obnovu souborů z files.bin...")
	count, err := restoreFiles(*dataPath, *restorePath, blobMap)
	if err != nil {
		log.Fatalf("Chyba při obnově: %v", err)
	}

	fmt.Printf("🎉 Hotovo! Obnoveno %d souborů do '%s'.\n", count, *restorePath)
}

// scanVolumes projde všechny .dat soubory a zaindexuje bloby
func scanVolumes(dir string) (map[int64]BlobLocation, error) {
	index := make(map[int64]BlobLocation)

	files, err := filepath.Glob(filepath.Join(dir, "volume_*.dat"))
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		baseName := filepath.Base(file)
		metaName := baseName[:len(baseName)-4] + ".meta" // volume_1.dat -> volume_1.meta
		metaPath := filepath.Join(dir, metaName)

		// Zkusíme použít META soubor pro rychlé skenování
		if _, err := os.Stat(metaPath); err == nil {
			fmt.Printf("  -> Rychlé skenování pomocí %s\n", metaName)
			if err := scanMetaFile(file, metaPath, index); err == nil {
				continue // Úspěch, jdeme na další volume
			}
			log.Printf("Varování: Chyba při čtení %s, přecházím na pomalé skenování .dat: %v", metaName, err)
		}

		fmt.Printf("  -> Pomalé skenování %s (chybí nebo vadný .meta)\n", baseName)
		scanDatFile(file, index)
	}

	return index, nil
}

func scanMetaFile(volPath, metaPath string, index map[int64]BlobLocation) error {
	f, err := os.Open(metaPath)
	if err != nil {
		return err
	}
	defer f.Close()

	recordSize := 29 // BlobID(8) + Offset(8) + Size(8) + Comp(1) + CRC(4)
	buf := make([]byte, recordSize)

	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		blobID := int64(binary.BigEndian.Uint64(buf[0:8]))
		offset := int64(binary.BigEndian.Uint64(buf[8:16]))
		size := int64(binary.BigEndian.Uint64(buf[16:24]))
		compAlg := buf[24]
		// crc := binary.BigEndian.Uint32(buf[25:29])

		// Offset v meta souboru ukazuje na začátek hlavičky v .dat souboru.
		// Pro čtení dat potřebujeme přeskočit hlavičku (HeaderSize).
		// Ale pozor: Store.WriteBlob vrací offset začátku hlavičky.
		// A naše struktura BlobLocation očekává offset začátku DAT.
		// Takže musíme přičíst HeaderSize.

		// HeaderSize musíme importovat nebo definovat. Zde natvrdo 22 (4+1+1+8+8).
		const HeaderSize = 22

		index[blobID] = BlobLocation{
			VolumePath:     volPath,
			Offset:         offset + int64(HeaderSize),
			SizeCompressed: size,
			CompAlg:        compAlg,
		}
	}
	return nil
}

func scanDatFile(file string, index map[int64]BlobLocation) {
	f, err := os.Open(file)
	if err != nil {
		log.Printf("Varování: Nelze otevřít %s: %v", file, err)
		return
	}
	defer f.Close()

	// Procházíme soubor blok po bloku
	for {
		// Získáme aktuální offset (začátek hlavičky)
		offset, _ := f.Seek(0, io.SeekCurrent)

		// Čteme hlavičku
		header := make([]byte, storage.HeaderSize)
		if _, err := io.ReadFull(f, header); err != nil {
			if err == io.EOF {
				break // Konec souboru
			}
			log.Printf("Chyba čtení hlavičky v %s: %v", file, err)
			break
		}

		magic := binary.BigEndian.Uint32(header[0:4])
		if magic != uint32(storage.MagicBytes) {
			log.Printf("Chyba: Neplatný magic number na offsetu %d v %s. Přeskakuji zbytek souboru.", offset, file)
			break
		}

		// ver := header[4]
		compAlg := header[5]
		size := int64(binary.BigEndian.Uint64(header[6:14]))
		blobID := int64(binary.BigEndian.Uint64(header[14:22]))

		// Uložíme do indexu (offset ukazuje na začátek dat, tj. za hlavičkou)
		index[blobID] = BlobLocation{
			VolumePath:     file,
			Offset:         offset + int64(storage.HeaderSize),
			SizeCompressed: size,
			CompAlg:        compAlg,
		}

		// Přeskočíme data a patičku
		if _, err := f.Seek(size+int64(storage.FooterSize), io.SeekCurrent); err != nil {
			break
		}
	}
}

// restoreFiles čte files.bin a obnovuje soubory
func restoreFiles(srcDir, dstDir string, blobIndex map[int64]BlobLocation) (int, error) {
	logPath := filepath.Join(srcDir, "files.bin")
	f, err := os.Open(logPath)
	if err != nil {
		return 0, fmt.Errorf("nelze otevřít files.bin: %w", err)
	}
	defer f.Close()

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return 0, err
	}

	restoredCount := 0
	decoder, _ := zstd.NewReader(nil)
	defer decoder.Close()

	for {
		// 1. Přečíst délku záznamu
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(f, lenBuf); err != nil {
			if err == io.EOF {
				break
			}
			return restoredCount, err
		}
		recordLen := binary.BigEndian.Uint32(lenBuf)

		// 2. Přečíst celý záznam
		record := make([]byte, recordLen)
		if _, err := io.ReadFull(f, record); err != nil {
			return restoredCount, err
		}

		// 3. Parsovat záznam (reverzní inženýrství logger.go)
		// ID Len (2)
		idLen := binary.BigEndian.Uint16(record[0:2])
		// ID (idLen)
		// id := string(record[2 : 2+idLen])
		cursor := 2 + int(idLen)

		// BlobID (8)
		blobID := int64(binary.BigEndian.Uint64(record[cursor : cursor+8]))
		cursor += 8

		// CreatedAt (8)
		cursor += 8

		// Flags (1)
		flags := record[cursor]
		cursor += 1

		// Optional fields based on flags
		if flags&(1<<0) != 0 { // OldCumulusID
			cursor += 8
		}
		if flags&(1<<1) != 0 { // ExpiresAt
			cursor += 8
		}

		// Name Len (2)
		nameLen := binary.BigEndian.Uint16(record[cursor : cursor+2])
		cursor += 2

		// Name
		filename := string(record[cursor : cursor+int(nameLen)])

		// 4. Obnovit soubor
		loc, exists := blobIndex[blobID]
		if !exists {
			log.Printf("❌ Chyba: BlobID %d pro soubor '%s' nebyl nalezen ve volumech.", blobID, filename)
			continue
		}

		if err := extractFile(dstDir, filename, loc, decoder); err != nil {
			log.Printf("❌ Chyba při extrakci '%s': %v", filename, err)
		} else {
			// fmt.Printf("Obnoven: %s\n", filename)
			restoredCount++
		}
	}

	return restoredCount, nil
}

func extractFile(dstDir, filename string, loc BlobLocation, zstdDecoder *zstd.Decoder) error {
	// Otevřít volume
	vol, err := os.Open(loc.VolumePath)
	if err != nil {
		return err
	}
	defer vol.Close()

	// Skočit na data
	if _, err := vol.Seek(loc.Offset, 0); err != nil {
		return err
	}

	// Omezit čtení jen na velikost blobu
	limitReader := io.LimitReader(vol, loc.SizeCompressed)

	// Připravit výstupní soubor
	outPath := filepath.Join(dstDir, filename)
	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	// Dekomprese
	switch loc.CompAlg {
	case 0: // None
		_, err = io.Copy(outFile, limitReader)
	case 1: // Gzip
		gz, err := gzip.NewReader(limitReader)
		if err != nil {
			return err
		}
		defer gz.Close()
		_, err = io.Copy(outFile, gz)
	case 2: // Zstd
		if err := zstdDecoder.Reset(limitReader); err != nil {
			return err
		}
		_, err = io.Copy(outFile, zstdDecoder)
	default:
		return fmt.Errorf("neznámá komprese: %d", loc.CompAlg)
	}

	return err
}
