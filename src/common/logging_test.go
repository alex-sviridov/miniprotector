package common

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSanitizeTag tests tag sanitization functionality
func TestSanitizeTag(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty tag", "", ""},
		{"valid alphanumeric", "web123", "web123"},
		{"valid with dots", "web.server.v1", "web.server.v1"},
		{"valid with dashes", "web-server-api", "web-server-api"},
		{"valid with underscores", "web_server_api", "web_server_api"},
		{"mixed valid chars", "web-server_v1.2", "web-server_v1.2"},
		{"invalid chars removed", "web@server#1!", "webserver1"},
		{"spaces removed", "web server", "webserver"},
		{"special chars removed", "web/server\\api", "webserverapi"},
		{"only invalid chars", "@#$%", ""},
		{"unicode removed", "web服务器", "web"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeTag(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeTag(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestNewLogger tests logger creation with various configurations
func TestNewLogger(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		config      *Config
		appName     string
		tag         string
		debugMode   bool
		quietMode   bool
		expectFile  bool
		expectError bool
	}{
		{
			name:       "valid config with file",
			config:     &Config{LogFolder: tempDir},
			appName:    "testapp",
			tag:        "web-server",
			debugMode:  false,
			quietMode:  false,
			expectFile: true,
		},
		{
			name:       "valid config quiet mode",
			config:     &Config{LogFolder: tempDir},
			appName:    "testapp",
			tag:        "api",
			debugMode:  true,
			quietMode:  true,
			expectFile: true,
		},
		{
			name:       "no log folder",
			config:     &Config{LogFolder: ""},
			appName:    "testapp",
			tag:        "test",
			debugMode:  false,
			quietMode:  false,
			expectFile: false,
		},
		{
			name:       "invalid log folder",
			config:     &Config{LogFolder: "/nonexistent/folder"},
			appName:    "testapp",
			tag:        "test",
			debugMode:  false,
			quietMode:  false,
			expectFile: false,
		},
		{
			name:       "empty tag",
			config:     &Config{LogFolder: tempDir},
			appName:    "testapp",
			tag:        "",
			debugMode:  false,
			quietMode:  false,
			expectFile: true,
		},
		{
			name:       "tag with invalid chars",
			config:     &Config{LogFolder: tempDir},
			appName:    "testapp",
			tag:        "web@server#1!",
			debugMode:  false,
			quietMode:  false,
			expectFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.config, tt.appName, tt.tag, tt.debugMode, tt.quietMode)

			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if logger == nil {
				t.Fatal("logger should not be nil")
			}

			// Check if log file was created
			if tt.expectFile && tt.config.LogFolder != "" {
				if logger.logFile == nil {
					// Only expect log file in quiet mode or when log folder is valid
					if tt.quietMode && tt.config.LogFolder == tempDir {
						t.Error("expected log file to be created in quiet mode")
					}
				} else {
					// Verify filename format
					filename := filepath.Base(logger.logFile.Name())
					timestamp := time.Now().Format("2006-01-02")
					expectedPattern := fmt.Sprintf(`%s-%s\.%d\.log`, regexp.QuoteMeta(tt.appName), timestamp, logger.pid)
					matched, _ := regexp.MatchString(expectedPattern, filename)
					if !matched {
						t.Errorf("filename %q doesn't match expected pattern %q", filename, expectedPattern)
					}
				}
			}

			// Check tag sanitization
			expectedTag := sanitizeTag(tt.tag)
			if logger.tag != expectedTag {
				t.Errorf("logger.tag = %q, want %q", logger.tag, expectedTag)
			}

			// Check debug mode
			if logger.debugMode != tt.debugMode {
				t.Errorf("logger.debugMode = %t, want %t", logger.debugMode, tt.debugMode)
			}

			// Check PID
			if logger.pid <= 0 {
				t.Error("logger.pid should be positive")
			}

			// Close at the end (no defer to avoid double close)
			logger.Close()
		})
	}
}

// TestLogFormatting tests log message formatting
func TestLogFormatting(t *testing.T) {
	tempDir := t.TempDir()
	config := &Config{LogFolder: tempDir}

	tests := []struct {
		name         string
		tag          string
		debugMode    bool
		logFunc      func(*Logger)
		expectTag    bool
		expectCaller bool
	}{
		{
			name:         "info log without tag",
			tag:          "",
			debugMode:    false,
			logFunc:      func(l *Logger) { l.Info("test message") },
			expectTag:    false,
			expectCaller: false,
		},
		{
			name:         "info log with tag",
			tag:          "web-server",
			debugMode:    false,
			logFunc:      func(l *Logger) { l.Info("test message") },
			expectTag:    true,
			expectCaller: false,
		},
		{
			name:         "debug log with tag (debug mode off)",
			tag:          "api",
			debugMode:    false,
			logFunc:      func(l *Logger) { l.Debug("debug message") },
			expectTag:    true,
			expectCaller: true, // Should have caller info when debug logs are written
		},
		{
			name:         "debug log with tag (debug mode on)",
			tag:          "api",
			debugMode:    true,
			logFunc:      func(l *Logger) { l.Debug("debug message") },
			expectTag:    true,
			expectCaller: true,
		},
		{
			name:         "error log with tag",
			tag:          "database",
			debugMode:    false,
			logFunc:      func(l *Logger) { l.Error("error message") },
			expectTag:    true,
			expectCaller: true,
		},
		{
			name:         "error log without tag",
			tag:          "",
			debugMode:    false,
			logFunc:      func(l *Logger) { l.Error("error message") },
			expectTag:    false,
			expectCaller: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(config, "testapp", tt.tag, tt.debugMode, true) // quiet mode to log only to file
			if err != nil {
				t.Fatalf("failed to create logger: %v", err)
			}

			// Call the log function
			tt.logFunc(logger)

			// For debug logs when debug mode is off, they won't be written
			if strings.Contains(tt.name, "debug") && !tt.debugMode {
				logger.Close()
				return // Skip content verification as no log should be written
			}

			// Get the expected consolidated filename after Close()
			timestamp := time.Now().Format("2006-01-02")
			consolidatedFile := filepath.Join(tempDir, fmt.Sprintf("testapp-%s.log", timestamp))

			// Close logger to flush and consolidate
			logger.Close()

			// Read consolidated log file content
			content, err := os.ReadFile(consolidatedFile)
			if err != nil {
				t.Fatalf("failed to read consolidated log file %s: %v", consolidatedFile, err)
			}

			logLine := strings.TrimSpace(string(content))
			if logLine == "" {
				t.Fatal("no log content found in consolidated file")
			}

			// Check timestamp format
			timestampPattern := `\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`
			if !regexp.MustCompile(timestampPattern).MatchString(logLine) {
				t.Errorf("log line missing proper timestamp: %q", logLine)
			}

			// Check PID (use strings.Contains, not regex)
			pidPattern := fmt.Sprintf("[%s:%d]", logger.appName, logger.pid)
			if !strings.Contains(logLine, pidPattern) {
				t.Errorf("log line missing PID %q: %q", pidPattern, logLine)
			}

			// Check tag
			if tt.expectTag && tt.tag != "" {
				tagPattern := fmt.Sprintf("[%s]", sanitizeTag(tt.tag))
				if !strings.Contains(logLine, tagPattern) {
					t.Errorf("log line missing tag %q: %q", tagPattern, logLine)
				}
			}

			// Check caller info (function name with parentheses)
			if tt.expectCaller {
				callerPattern := `\w+\(\)`
				if !regexp.MustCompile(callerPattern).MatchString(logLine) {
					t.Errorf("log line missing caller info pattern %q: %q", callerPattern, logLine)
				}
			}

			// Check log level
			if strings.Contains(tt.name, "info") {
				if !strings.Contains(logLine, "[INFO]") {
					t.Errorf("info log missing [INFO] level: %q", logLine)
				}
			} else if strings.Contains(tt.name, "debug") {
				if !strings.Contains(logLine, "[DEBUG]") {
					t.Errorf("debug log missing [DEBUG] level: %q", logLine)
				}
			} else if strings.Contains(tt.name, "error") {
				if !strings.Contains(logLine, "[ERROR]") {
					t.Errorf("error log missing [ERROR] level: %q", logLine)
				}
			}
		})
	}
}

// TestDebugMode tests debug mode functionality
func TestDebugMode(t *testing.T) {
	tempDir := t.TempDir()
	config := &Config{LogFolder: tempDir}

	t.Run("debug mode off - no debug logs", func(t *testing.T) {
		logger, err := NewLogger(config, "testapp", "test", false, true)
		if err != nil {
			t.Fatalf("failed to create logger: %v", err)
		}

		logger.Debug("this should not appear")

		// Get consolidated filename
		timestamp := time.Now().Format("2006-01-02")
		consolidatedFile := filepath.Join(tempDir, fmt.Sprintf("testapp-%s.log", timestamp))

		logger.Close()

		// Check if consolidated file exists and is empty
		if _, err := os.Stat(consolidatedFile); err == nil {
			content, err := os.ReadFile(consolidatedFile)
			if err != nil {
				t.Fatalf("failed to read consolidated log file: %v", err)
			}

			if len(content) > 0 {
				t.Errorf("expected no debug logs when debug mode is off, got: %q", string(content))
			}
		}
		// If no consolidated file exists, that's also fine (no logs were written)
	})

	t.Run("debug mode on - debug logs appear", func(t *testing.T) {
		logger, err := NewLogger(config, "testapp2", "test", true, true)
		if err != nil {
			t.Fatalf("failed to create logger: %v", err)
		}

		logger.Debug("this should appear")

		// Get consolidated filename
		timestamp := time.Now().Format("2006-01-02")
		consolidatedFile := filepath.Join(tempDir, fmt.Sprintf("testapp2-%s.log", timestamp))

		logger.Close()

		content, err := os.ReadFile(consolidatedFile)
		if err != nil {
			t.Fatalf("failed to read consolidated log file: %v", err)
		}

		logLine := strings.TrimSpace(string(content))
		if !strings.Contains(logLine, "[DEBUG]") || !strings.Contains(logLine, "this should appear") {
			t.Errorf("expected debug log when debug mode is on, got: %q", logLine)
		}
	})
}

// TestGenerateTargetFileName tests target filename generation
func TestGenerateTargetFileName(t *testing.T) {
	logger := &Logger{pid: 1234}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid PID filename",
			input:    "myapp-2006-01-02.1234.log",
			expected: "myapp-2006-01-02.log",
		},
		{
			name:     "complex app name",
			input:    "my-complex-app-2006-01-02.1234.log",
			expected: "my-complex-app-2006-01-02.log",
		},
		{
			name:     "different PID",
			input:    "myapp-2006-01-02.5678.log",
			expected: "", // Should fail because PID doesn't match
		},
		{
			name:     "no PID suffix",
			input:    "myapp-2006-01-02.log",
			expected: "", // Should fail because no PID suffix
		},
		{
			name:     "invalid extension",
			input:    "myapp-2006-01-02.1234.txt",
			expected: "", // Should fail because not .log
		},
		{
			name:     "empty filename",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := logger.generateTargetFileName(tt.input)
			if result != tt.expected {
				t.Errorf("generateTargetFileName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestLogConsolidation tests log file consolidation on close
func TestLogConsolidation(t *testing.T) {
	tempDir := t.TempDir()
	config := &Config{LogFolder: tempDir}

	t.Run("first logger renames file", func(t *testing.T) {
		logger, err := NewLogger(config, "testapp", "test", false, true)
		if err != nil {
			t.Fatalf("failed to create logger: %v", err)
		}

		logger.Info("test message")
		originalFile := logger.logFile.Name()

		logger.Close()

		// Original file should be renamed
		if _, err := os.Stat(originalFile); !os.IsNotExist(err) {
			t.Error("original PID file should be removed after consolidation")
		}

		// Target file should exist
		targetFile := filepath.Join(tempDir, "testapp-"+time.Now().Format("2006-01-02")+".log")
		if _, err := os.Stat(targetFile); os.IsNotExist(err) {
			t.Error("target consolidated file should exist")
		}

		// Check content
		content, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("failed to read target file: %v", err)
		}

		if !strings.Contains(string(content), "test message") {
			t.Error("consolidated file should contain original log message")
		}
	})

	t.Run("second logger appends to existing file", func(t *testing.T) {
		// Create first logger to establish target file
		logger1, err := NewLogger(config, "testapp2", "test", false, true)
		if err != nil {
			t.Fatalf("failed to create logger1: %v", err)
		}
		logger1.Info("message from logger1")
		logger1.Close()

		// Create second logger
		logger2, err := NewLogger(config, "testapp2", "test", false, true)
		if err != nil {
			t.Fatalf("failed to create logger2: %v", err)
		}
		logger2.Info("message from logger2")

		originalFile2 := logger2.logFile.Name()
		logger2.Close()

		// Original PID file should be removed
		if _, err := os.Stat(originalFile2); !os.IsNotExist(err) {
			t.Error("second logger's PID file should be removed after consolidation")
		}

		// Target file should contain both messages
		targetFile := filepath.Join(tempDir, "testapp2-"+time.Now().Format("2006-01-02")+".log")
		content, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("failed to read target file: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "message from logger1") {
			t.Error("consolidated file should contain message from logger1")
		}
		if !strings.Contains(contentStr, "message from logger2") {
			t.Error("consolidated file should contain message from logger2")
		}
	})
}

// TestLoggerMethods tests the basic logging methods
func TestLoggerMethods(t *testing.T) {
	tempDir := t.TempDir()
	config := &Config{LogFolder: tempDir}

	logger, err := NewLogger(config, "testapp", "test", true, true)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	// Test all log levels
	logger.Info("info message with %s", "formatting")
	logger.Debug("debug message with %d", 42)
	logger.Error("error message")

	// Test getter methods
	if logger.GetPID() <= 0 {
		t.Error("GetPID() should return positive PID")
	}

	if logger.GetTag() != "test" {
		t.Errorf("GetTag() = %q, want %q", logger.GetTag(), "test")
	}

	// Get consolidated filename
	timestamp := time.Now().Format("2006-01-02")
	consolidatedFile := filepath.Join(tempDir, fmt.Sprintf("testapp-%s.log", timestamp))

	logger.Close()

	// Verify log content in consolidated file
	content, err := os.ReadFile(consolidatedFile)
	if err != nil {
		t.Fatalf("failed to read consolidated log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 log lines, got %d", len(lines))
	}

	// Check each log level
	hasInfo := false
	hasDebug := false
	hasError := false

	for _, line := range lines {
		if strings.Contains(line, "[INFO]") && strings.Contains(line, "info message") {
			hasInfo = true
		}
		if strings.Contains(line, "[DEBUG]") && strings.Contains(line, "debug message") {
			hasDebug = true
		}
		if strings.Contains(line, "[ERROR]") && strings.Contains(line, "error message") {
			hasError = true
		}
	}

	if !hasInfo {
		t.Error("INFO log not found in output")
	}
	if !hasDebug {
		t.Error("DEBUG log not found in output")
	}
	if !hasError {
		t.Error("ERROR log not found in output")
	}
}

// BenchmarkLogInfo benchmarks INFO logging performance
func BenchmarkLogInfo(b *testing.B) {
	tempDir := b.TempDir()
	config := &Config{LogFolder: tempDir}

	logger, err := NewLogger(config, "benchapp", "", false, true)
	if err != nil {
		b.Fatalf("failed to create logger: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message %d", i)
	}

	logger.Close()
}

// BenchmarkLogError benchmarks ERROR logging performance (with caller info)
func BenchmarkLogError(b *testing.B) {
	tempDir := b.TempDir()
	config := &Config{LogFolder: tempDir}

	logger, err := NewLogger(config, "benchapp", "", false, true)
	if err != nil {
		b.Fatalf("failed to create logger: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Error("benchmark error %d", i)
	}

	logger.Close()
}

// BenchmarkLogDebugDisabled benchmarks DEBUG logging when debug mode is disabled
func BenchmarkLogDebugDisabled(b *testing.B) {
	tempDir := b.TempDir()
	config := &Config{LogFolder: tempDir}

	logger, err := NewLogger(config, "benchapp", "", false, true) // debug mode off
	if err != nil {
		b.Fatalf("failed to create logger: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Debug("benchmark debug %d", i) // Should be fast since debug is disabled
	}

	logger.Close()
}
