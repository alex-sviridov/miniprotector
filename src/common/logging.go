package common

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Logger provides structured logging with performance optimizations for concurrent usage.
// Key features:
// - Process-specific log files (appName-timestamp.PID.log) prevent conflicts
// - Tags appear in log lines for easy filtering, not in filenames
// - Performance-optimized: INFO logs skip expensive caller info
// - Log consolidation: PID files merge into final logs on application shutdown
// - Thread-safe file operations with proper error handling
//
// Log format examples:
// - With tag:    2006/01/02 15:04:05 [INFO] [PID:1234] [web-server] Server started
// - Without tag: 2006/01/02 15:04:05 [ERROR] [PID:1234] connectDB() Database connection failed
type Logger struct {
	infoLogger  *log.Logger
	debugLogger *log.Logger
	errorLogger *log.Logger
	debugMode   bool
	logFile     *os.File
	appName     string
	pid         int
	tag         string
}

// sanitizeTag ensures tag contains only alphanumeric characters, dots, dashes, and underscores
func sanitizeTag(tag string) string {
	if tag == "" {
		return ""
	}

	var result strings.Builder
	for _, char := range tag {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '-' || char == '_' {
			result.WriteRune(char)
		}
	}

	sanitized := result.String()
	if sanitized == "" {
		return ""
	}
	return sanitized
}

// logInitError logs initialization errors using the same format as regular logs
func logInitError(format string, v ...interface{}) {
	// Create a temporary logger that writes to stderr for initialization errors
	tempLogger := &Logger{
		errorLogger: log.New(os.Stderr, "", 0),
		pid:         os.Getpid(),
	}

	// Use the same formatting as regular error messages (with caller info for debugging)
	logMessage := tempLogger.formatLogMessage("ERROR", true, format, v...)
	tempLogger.errorLogger.Print(logMessage)
}

// NewLogger creates a new logger instance with optional tag for log lines
// File naming pattern:
// - During runtime: appName-timestamp.PID.log
// - After close: appName-timestamp.log
//
// The tag parameter is included in each log line for identification instead of filename.
// Tags are sanitized to contain only alphanumeric characters, dots, dashes, and underscores.
//
// Each process gets its own log file identified by PID suffix, making it safe for concurrent usage
// across multiple process instances.
func NewLogger(config *Config, appName string, tag string, debugMode bool, quietMode bool) (*Logger, error) {
	var logOutput io.Writer = io.Discard
	var logFile *os.File

	// Get current process ID
	pid := os.Getpid()

	// Sanitize tag to ensure it only contains safe characters
	sanitizedTag := sanitizeTag(tag)

	// Check if log folder exists and is writable
	if config.LogFolder != "" {
		if stat, err := os.Stat(config.LogFolder); err == nil && stat.IsDir() {
			// Create log file with app name, timestamp, and PID suffix
			timestamp := time.Now().Format("2006-01-02")
			logFileName := fmt.Sprintf("%s-%s.%d.log", appName, timestamp, pid)
			logFilePath := filepath.Join(config.LogFolder, logFileName)

			file, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil && !quietMode {
				// Use formatted error message instead of plain fmt.Fprintf
				logInitError("Cannot write to log folder %s: %v. Logging to stdout.", config.LogFolder, err)
				logOutput = os.Stdout
			} else if err != nil && quietMode {
				// Even in quiet mode, we might want to log this error to stderr
				logInitError("Cannot write to log folder %s: %v. Logging disabled.", config.LogFolder, err)
				logOutput = io.Discard
			} else if quietMode {
				logOutput = file
				logFile = file
			} else {
				logOutput = io.MultiWriter(os.Stdout, file)
				logFile = file
			}
		} else if !quietMode {
			logInitError("Log folder %s does not exist or is not accessible. Logging to stdout.", config.LogFolder)
			logOutput = os.Stdout
		}
	}

	// Create loggers without file/line flags since we'll add them manually
	logger := &Logger{
		infoLogger:  log.New(logOutput, "", 0),
		debugLogger: log.New(logOutput, "", 0),
		errorLogger: log.New(logOutput, "", 0),
		debugMode:   debugMode,
		logFile:     logFile,
		pid:         pid,
		appName:     appName,
		tag:         sanitizedTag,
	}

	return logger, nil
}

// TODO: Investigate github.com/tlog-dev/loc instead of runtime as it's said to be more performant
// getCallerInfo returns formatted caller information (function name only for performance)
func (l *Logger) getCallerInfo(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
	if !ok {
		return "unknown()"
	}

	// Get function name only (no file:line for better performance)
	funcName := "unknown"
	if fn := runtime.FuncForPC(pc); fn != nil {
		fullFuncName := fn.Name()
		// Extract just the function name (remove package path)
		if idx := strings.LastIndex(fullFuncName, "."); idx != -1 {
			funcName = fullFuncName[idx+1:]
		} else {
			funcName = fullFuncName
		}
	}

	return fmt.Sprintf("%s()", funcName)
}

// getCallerInfoIfNeeded returns caller info only when needed for debugging
func (l *Logger) getCallerInfoIfNeeded(skip int, include bool) string {
	if !include {
		return ""
	}
	return l.getCallerInfo(skip)
}

// formatLogMessage creates a formatted log message with timestamp, level, PID, tag, and optional caller info
func (l *Logger) formatLogMessage(level string, includeCallerInfo bool, format string, v ...interface{}) string {
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	message := fmt.Sprintf(format, v...)

	// Build the log line with optional tag and caller info
	var logLine string
	if l.tag != "" {
		if includeCallerInfo {
			callerInfo := l.getCallerInfoIfNeeded(4, true) // Skip runtime.Caller, getCallerInfo, getCallerInfoIfNeeded, formatLogMessage
			logLine = fmt.Sprintf("%s [%s] [%s:%d] [%s] %s %s", timestamp, level, l.appName, l.pid, l.tag, callerInfo, message)
		} else {
			logLine = fmt.Sprintf("%s [%s] [%s:%d] [%s] %s", timestamp, level, l.appName, l.pid, l.tag, message)
		}
	} else {
		if includeCallerInfo {
			callerInfo := l.getCallerInfoIfNeeded(4, true) // Skip runtime.Caller, getCallerInfo, getCallerInfoIfNeeded, formatLogMessage
			logLine = fmt.Sprintf("%s [%s] [%s:%d] %s %s", timestamp, level, l.appName, l.pid, callerInfo, message)
		} else {
			logLine = fmt.Sprintf("%s [%s] [%s:%d] %s", timestamp, level, l.appName, l.pid, message)
		}
	}

	return logLine
}

// Close closes the log file and consolidates PID-specific logs into final log files
// This bundles temporary PID log files into permanent logs:
// - If target file exists: appends current PID log content to it
// - If target file doesn't exist: renames current PID log to remove PID
// Target pattern: appName-timestamp.log (without .PID suffix)
func (l *Logger) Close() {
	if l.logFile == nil {
		return
	}

	// Get current log file path before closing
	currentLogPath := l.logFile.Name()

	// Close the current log file
	if err := l.logFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Error closing log file %s: %v\n", currentLogPath, err)
		return
	}

	// Generate target filename without PID
	dir := filepath.Dir(currentLogPath)
	currentFileName := filepath.Base(currentLogPath)

	// Parse current filename to construct target filename
	// Current format: appName-timestamp.PID.log
	// Target format:  appName-timestamp.log
	targetFileName := l.generateTargetFileName(currentFileName)
	if targetFileName == "" {
		fmt.Fprintf(os.Stderr, "Warning: Could not generate target filename for %s\n", currentLogPath)
		return
	}

	targetLogPath := filepath.Join(dir, targetFileName)

	// Check if target file exists
	if _, err := os.Stat(targetLogPath); err == nil {
		// Target exists - append current log content to it
		if err := l.appendLogFile(currentLogPath, targetLogPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to append log file %s to %s: %v\n", currentLogPath, targetLogPath, err)
			return
		}

		// Remove the temporary PID log file
		if err := os.Remove(currentLogPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to remove temporary log file %s: %v\n", currentLogPath, err)
		}
	} else {
		// Target doesn't exist - rename current file to target
		if err := os.Rename(currentLogPath, targetLogPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to rename log file %s to %s: %v\n", currentLogPath, targetLogPath, err)
		}
	}
}

// generateTargetFileName creates the target filename by removing PID suffix from current filename
// Converts: appName-timestamp.PID.log -> appName-timestamp.log
func (l *Logger) generateTargetFileName(currentFileName string) string {
	// Remove .log extension
	if !strings.HasSuffix(currentFileName, ".log") {
		return "" // Invalid format - must end with .log
	}
	nameWithoutLogExt := strings.TrimSuffix(currentFileName, ".log")

	// Check if it ends with .PID
	pidSuffix := fmt.Sprintf(".%d", l.pid)
	if !strings.HasSuffix(nameWithoutLogExt, pidSuffix) {
		return "" // PID suffix not found or doesn't match
	}

	// Remove PID suffix and add back .log extension
	targetName := strings.TrimSuffix(nameWithoutLogExt, pidSuffix)
	return targetName + ".log"
}

// appendLogFile appends the contents of sourceFile to targetFile
func (l *Logger) appendLogFile(sourceFile, targetFile string) error {
	// Open source file for reading
	src, err := os.Open(sourceFile)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer src.Close()

	// Open target file for appending
	dst, err := os.OpenFile(targetFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open target file for append: %w", err)
	}
	defer dst.Close()

	// Copy contents from source to target
	_, err = io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("failed to copy log contents: %w", err)
	}

	return nil
}

// GetTag returns the sanitized tag used for this logger instance
func (l *Logger) GetTag() string {
	return l.tag
}

// GetPID returns the process ID for this logger instance
func (l *Logger) GetPID() int {
	return l.pid
}

// Info logs info level messages (fast path - no caller info)
func (l *Logger) Info(format string, v ...interface{}) {
	logMessage := l.formatLogMessage("INFO", false, format, v...)
	l.infoLogger.Print(logMessage)
}

// Debug logs debug level messages with caller info (only if debug mode is enabled)
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.debugMode {
		logMessage := l.formatLogMessage("DEBUG", true, format, v...)
		l.debugLogger.Print(logMessage)
	}
}

// Error logs error level messages with caller info (for debugging critical issues)
func (l *Logger) Error(format string, v ...interface{}) {
	logMessage := l.formatLogMessage("ERROR", true, format, v...)
	l.errorLogger.Print(logMessage)
}
