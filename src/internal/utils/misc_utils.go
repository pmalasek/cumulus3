package utils

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// GetOutboundIP gets the preferred outbound ip of this machine
func GetOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String()
}

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

// ParseValidity parses a validity string (e.g. "1 day", "2 months") into a time.Time
func ParseValidity(val string) (time.Time, error) {
	parts := strings.Fields(val)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid format")
	}
	amount, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid amount")
	}
	unit := strings.ToLower(parts[1])

	var d time.Duration
	switch {
	case strings.HasPrefix(unit, "day"):
		d = time.Duration(amount) * 24 * time.Hour
	case strings.HasPrefix(unit, "month"):
		d = time.Duration(amount) * 30 * 24 * time.Hour // Approx
	default:
		return time.Time{}, fmt.Errorf("unknown unit")
	}

	if d < 24*time.Hour {
		return time.Time{}, fmt.Errorf("minimum validity is 1 day")
	}
	if d > 365*24*time.Hour {
		return time.Time{}, fmt.Errorf("maximum validity is 1 year")
	}

	return time.Now().Add(d), nil
}
