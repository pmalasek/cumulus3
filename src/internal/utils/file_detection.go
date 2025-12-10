package utils

import (
	"bytes"
	"regexp"
	"strings"
)

type FileTypeResult struct {
	Type        string
	Subtype     string
	ContentType string
}

type PatternDefinition struct {
	Pattern []byte
	Offset  int
	Result  FileTypeResult
}

var filePatterns = []PatternDefinition{
	// PDF
	{Pattern: []byte{0x25, 0x50, 0x44, 0x46}, Result: FileTypeResult{Type: "pdf", ContentType: "application/pdf"}},

	// Images
	{Pattern: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, Result: FileTypeResult{Type: "image", Subtype: "PNG", ContentType: "image/png"}},
	{Pattern: []byte{0xFF, 0xD8, 0xFF}, Result: FileTypeResult{Type: "image", Subtype: "JPEG", ContentType: "image/jpeg"}},
	{Pattern: []byte{0x47, 0x49, 0x46, 0x38}, Result: FileTypeResult{Type: "image", Subtype: "GIF", ContentType: "image/gif"}},
	{Pattern: []byte{0x42, 0x4D}, Result: FileTypeResult{Type: "image", Subtype: "BMP", ContentType: "image/bmp"}},
	{Pattern: []byte{0x49, 0x49, 0x2A, 0x00}, Result: FileTypeResult{Type: "image", Subtype: "TIFF", ContentType: "image/tiff"}},
	{Pattern: []byte{0x4D, 0x4D, 0x00, 0x2A}, Result: FileTypeResult{Type: "image", Subtype: "TIFF", ContentType: "image/tiff"}},
	{Pattern: []byte{0x00, 0x00, 0x01, 0x00}, Result: FileTypeResult{Type: "image", Subtype: "ICO", ContentType: "image/x-icon"}},

	// ECU Files
	{Pattern: []byte{0x2E, 0x71, 0xD4, 0x12, 0x2F, 0x7D, 0xD6, 0x08, 0x49, 0x34}, Result: FileTypeResult{Type: "ecu", Subtype: "KESSv2", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0xEC, 0xBB, 0x56, 0x0C, 0x9C, 0x59, 0xE9, 0x42, 0x41, 0x4F, 0x00, 0x05, 0xF8, 0xE8, 0xBF, 0x8D}, Result: FileTypeResult{Type: "ecu", Subtype: "KESSv3", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0xEC, 0xBB, 0x56, 0x0C, 0x9C, 0x59, 0xE9, 0x42, 0x41, 0x4F, 0x80, 0x05, 0xF0, 0x6D, 0xBF, 0x8D}, Result: FileTypeResult{Type: "ecu", Subtype: "KESSv3", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0x4D, 0x4D, 0x53, 0x46, 0x01, 0x00, 0x00, 0x00, 0x00, 0x01}, Result: FileTypeResult{Type: "ecu", Subtype: "FlexMagic", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0xA5, 0x3B, 0xFB, 0x19, 0xAC, 0x26}, Result: FileTypeResult{Type: "ecu", Subtype: "KTag", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0x45, 0x42, 0x03, 0x04}, Result: FileTypeResult{Type: "ecu", Subtype: "ZPR", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0x50, 0x4D, 0x03, 0x04}, Result: FileTypeResult{Type: "ecu", Subtype: "ECU", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0x58, 0x42, 0x03, 0x04}, Result: FileTypeResult{Type: "ecu", Subtype: "XPR", ContentType: "application/octet-stream"}},
	{Pattern: []byte{0x58, 0x49, 0x03, 0x04}, Result: FileTypeResult{Type: "ecu", Subtype: "XP2", ContentType: "application/octet-stream"}},

	// Archive
	{Pattern: []byte{0x50, 0x4B, 0x03, 0x04}, Result: FileTypeResult{Type: "binary", Subtype: "ZIP", ContentType: "application/zip"}},
}

func matchesPattern(data []byte, pattern []byte, offset int) bool {
	if len(data) < offset+len(pattern) {
		return false
	}
	return bytes.Equal(data[offset:offset+len(pattern)], pattern)
}

func DetectFileType(data []byte) FileTypeResult {
	// Kontrola magic bytes pomocí konfigurace
	for _, def := range filePatterns {
		if matchesPattern(data, def.Pattern, def.Offset) {
			return def.Result
		}
	}

	// WebP - speciální kontrola (RIFF na pozici 0, WEBP na pozici 8)
	if len(data) >= 12 &&
		matchesPattern(data, []byte{0x52, 0x49, 0x46, 0x46}, 0) &&
		matchesPattern(data, []byte{0x57, 0x45, 0x42, 0x50}, 8) {
		return FileTypeResult{Type: "image", Subtype: "WebP", ContentType: "image/webp"}
	}

	// SVG - kontrola XML hlavičky
	// Check first 100 bytes
	limit := 100
	if len(data) < limit {
		limit = len(data)
	}
	headerText := string(data[:limit])
	if strings.Contains(headerText, "<svg") || strings.Contains(headerText, "<?xml") {
		return FileTypeResult{Type: "image", Subtype: "SVG", ContentType: "image/svg+xml"}
	}

	// Fake file detection
	if len(data) < 120 {
		text := string(data)
		if strings.Contains(text, "gaia_fake_file") {
			return FileTypeResult{Type: "binary", Subtype: "Fake", ContentType: "application/octet-stream"}
		}
	}

	// Ident file detection
	if len(data) < 12000 {
		text := string(data)
		if !strings.Contains(text, "fake") {
			return FileTypeResult{Type: "binary", Subtype: "Ident", ContentType: "application/octet-stream"}
		}
	}

	// Text-based file detection
	limit = 1000
	if len(data) < limit {
		limit = len(data)
	}
	textSample := string(data[:limit])

	// Cummins CSV
	if strings.HasPrefix(textSample, "sep=,") &&
		strings.Contains(textSample, "Service Tool") &&
		strings.Contains(textSample, "INSITE") &&
		strings.Contains(textSample, "ECM Code") {
		return FileTypeResult{Type: "text", Subtype: "Cummins", ContentType: "text/csv"}
	}

	// CAT
	if strings.Contains(textSample, "Software Group Part Number") {
		matched, _ := regexp.MatchString(`C\d+(\.\d+)?`, textSample)
		if matched {
			return FileTypeResult{Type: "text", Subtype: "CAT", ContentType: "text/plain"}
		}
	}

	// Výchozí: binární soubor
	return FileTypeResult{Type: "binary", ContentType: "application/octet-stream"}
}
