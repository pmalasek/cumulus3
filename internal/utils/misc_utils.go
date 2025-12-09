package utils

import (
	"strconv"
	"strings"
)

// ParseBytes parses a string representation of bytes (e.g. "10MB", "1GB") into int64
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.ToUpper(s)

	var mult int64 = 1
	if strings.HasSuffix(s, "GB") {
		mult = 1 << 30
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "G") {
		mult = 1 << 30
		s = strings.TrimSuffix(s, "G")
	} else if strings.HasSuffix(s, "MB") {
		mult = 1 << 20
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "M") {
		mult = 1 << 20
		s = strings.TrimSuffix(s, "M")
	} else if strings.HasSuffix(s, "KB") {
		mult = 1 << 10
		s = strings.TrimSuffix(s, "KB")
	} else if strings.HasSuffix(s, "K") {
		mult = 1 << 10
		s = strings.TrimSuffix(s, "K")
	}

	s = strings.TrimSpace(s)
	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return val * mult, nil
}
