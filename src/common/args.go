package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseDestination parses destination string in format "host:port" or "port"
func ParseDestination(dest string, defaultHost string, defaultPort int) (string, int, error) {
	if dest == "" {
		return defaultHost, defaultPort, nil
	}

	parts := strings.Split(dest, ":")
	switch len(parts) {
	case 1:
		// Only port specified
		port, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", 0, fmt.Errorf("invalid port: %s", parts[0])
		}
		return defaultHost, port, nil
	case 2:
		// Host and port specified
		host := parts[0]
		if host == "" {
			host = defaultHost
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, fmt.Errorf("invalid port: %s", parts[1])
		}
		if err := ValidatePort(port); err != nil {
			return "", 0, fmt.Errorf("port error: %w", err)
		}
		return host, port, nil
	default:
		return "", 0, fmt.Errorf("invalid destination format: %s", dest)
	}
}

func ValidatePort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535, got %d", port)
	}

	return nil
}

// ValidateStreamsCount validates that streams count is positive
func ValidateStreamsCount(streams int) error {
	if streams <= 0 {
		return fmt.Errorf("streams count must be positive, got: %d", streams)
	}
	return nil
}

// ValidateSourceFolder validates that source folder exists and converts to absolute path
func ValidatePath(sourceFolder string) (string, error) {
	// Validate source folder exists
	if _, err := os.Stat(sourceFolder); os.IsNotExist(err) {
		return "", fmt.Errorf("source folder does not exist: %s", sourceFolder)
	}

	// Convert source folder to absolute path
	absSourceFolder, err := filepath.Abs(sourceFolder)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for source folder: %w", err)
	}

	return absSourceFolder, nil
}
