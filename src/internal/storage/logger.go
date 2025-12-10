package storage

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
)

// MetadataLogger handles appending file metadata to a recovery log
type MetadataLogger struct {
	LogPath string
	mu      sync.Mutex
}

// NewMetadataLogger creates a new logger instance
func NewMetadataLogger(baseDir string) *MetadataLogger {
	// Ensure directory exists
	_ = os.MkdirAll(baseDir, 0755)

	return &MetadataLogger{
		LogPath: filepath.Join(baseDir, "files_metadata.bin"),
	}
}

// LogFile appends the file metadata to the log file in binary format
func (l *MetadataLogger) LogFile(f File) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	file, err := os.OpenFile(l.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Příprava dat do bufferu
	// Odhad velikosti: ID(36) + BlobID(8) + Time(8) + Flags(1) + Opts(16) + NameLen(2) + Name(N)
	buf := make([]byte, 0, 128)

	// 1. ID (String length + String)
	idBytes := []byte(f.ID)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(idBytes)))
	buf = append(buf, idBytes...)

	// 2. BlobID
	buf = binary.BigEndian.AppendUint64(buf, uint64(f.BlobID))

	// 3. CreatedAt (Unix Nano)
	buf = binary.BigEndian.AppendUint64(buf, uint64(f.CreatedAt.UnixNano()))

	// 4. Flags & Optional fields
	var flags uint8 = 0
	if f.OldCumulusID != nil {
		flags |= 1 << 0 // Bit 0 set
	}
	if f.ExpiresAt != nil {
		flags |= 1 << 1 // Bit 1 set
	}
	if f.Tags != "" {
		flags |= 1 << 2 // Bit 2 set
	}
	buf = append(buf, flags)

	if f.OldCumulusID != nil {
		buf = binary.BigEndian.AppendUint64(buf, uint64(*f.OldCumulusID))
	}
	if f.ExpiresAt != nil {
		buf = binary.BigEndian.AppendUint64(buf, uint64(f.ExpiresAt.UnixNano()))
	}
	if f.Tags != "" {
		tagsBytes := []byte(f.Tags)
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(tagsBytes)))
		buf = append(buf, tagsBytes...)
	}

	// 5. Name
	nameBytes := []byte(f.Name)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(nameBytes)))
	buf = append(buf, nameBytes...)

	// Zápis délky celého záznamu (4 bytes) + samotný záznam
	totalLen := uint32(len(buf))
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, totalLen)

	if _, err := file.Write(lenBuf); err != nil {
		return err
	}
	if _, err := file.Write(buf); err != nil {
		return err
	}

	return nil
}
