package utils

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var (
	currentLogLevel LogLevel = INFO
	logLevelNames            = map[LogLevel]string{
		DEBUG: "DEBUG",
		INFO:  "INFO",
		WARN:  "WARN",
		ERROR: "ERROR",
	}
	logLevelColors = map[LogLevel]string{
		DEBUG: "\033[36m", // Cyan
		INFO:  "\033[32m", // Green
		WARN:  "\033[33m", // Yellow
		ERROR: "\033[31m", // Red
	}
	resetColor     = "\033[0m"
	useColors      = true
	structuredLogs = false
)

// InitLogger initializes the logger with settings from environment
func InitLogger() {
	// Set log level from environment
	logLevelStr := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch logLevelStr {
	case "DEBUG":
		currentLogLevel = DEBUG
	case "INFO":
		currentLogLevel = INFO
	case "WARN", "WARNING":
		currentLogLevel = WARN
	case "ERROR":
		currentLogLevel = ERROR
	default:
		currentLogLevel = INFO
	}

	// Check if we should use structured (JSON) logs
	if os.Getenv("LOG_FORMAT") == "json" {
		structuredLogs = true
		useColors = false
	}

	// Disable colors if not in TTY or explicitly disabled
	if os.Getenv("LOG_COLOR") == "false" || os.Getenv("NO_COLOR") != "" {
		useColors = false
	}

	log.SetFlags(0) // We'll handle our own formatting
}

// shouldLog checks if a message at the given level should be logged
func shouldLog(level LogLevel) bool {
	return level >= currentLogLevel
}

// formatMessage formats a log message with timestamp and level
func formatMessage(level LogLevel, category, format string, args ...interface{}) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	levelName := logLevelNames[level]

	if structuredLogs {
		// JSON format for production
		return fmt.Sprintf(`{"time":"%s","level":"%s","category":"%s","message":"%s"}`,
			timestamp, levelName, category, escapeJSON(message))
	}

	// Human-readable format
	if useColors {
		color := logLevelColors[level]
		return fmt.Sprintf("%s [%s%s%s] [%s] %s",
			timestamp, color, levelName, resetColor, category, message)
	}

	return fmt.Sprintf("%s [%s] [%s] %s",
		timestamp, levelName, category, message)
}

// escapeJSON escapes special characters for JSON
func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

// Debug logs a debug message
func Debug(category, format string, args ...interface{}) {
	if shouldLog(DEBUG) {
		log.Println(formatMessage(DEBUG, category, format, args...))
	}
}

// Info logs an info message
func Info(category, format string, args ...interface{}) {
	if shouldLog(INFO) {
		log.Println(formatMessage(INFO, category, format, args...))
	}
}

// Warn logs a warning message
func Warn(category, format string, args ...interface{}) {
	if shouldLog(WARN) {
		log.Println(formatMessage(WARN, category, format, args...))
	}
}

// Error logs an error message
func Error(category, format string, args ...interface{}) {
	if shouldLog(ERROR) {
		log.Println(formatMessage(ERROR, category, format, args...))
	}
}

// GetLogLevel returns current log level as string
func GetLogLevel() string {
	return logLevelNames[currentLogLevel]
}
