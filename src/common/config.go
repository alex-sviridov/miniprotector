package common

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds configuration from /etc/btool/local.conf
type Config struct {
	DefaultPort    int
	DefaultStreams int
	LogFolder      string
	ClientCRSQueryBatchSize int
}

// ParseConfig reads configuration from the specified config file
// Returns error if config file doesn't exist or required fields are missing
func ParseConfig(configPath string) (*Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file %s: %w", configPath, err)
	}
	defer file.Close()

	config := &Config{}
	foundFields := make(map[string]bool)

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key=value pairs
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format at line %d: %s", lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "default_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid default_port value at line %d: %s", lineNum, value)
			}
			config.DefaultPort = port
			foundFields["default_port"] = true
		case "default_streams":
			streams, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid default_streams value at line %d: %s", lineNum, value)
			}
			config.DefaultStreams = streams
			foundFields["default_streams"] = true
		case "logfolder":
			config.LogFolder = value
			foundFields["logfolder"] = true
		case "ClientCRSQueryBatchSize":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid ClientCRSQueryBatchSize value at line %d: %s", lineNum, value)
			}
			config.ClientCRSQueryBatchSize = number
			foundFields["ClientCRSQueryBatchSize"] = true
		default:
			return nil, fmt.Errorf("unknown configuration key at line %d: %s", lineNum, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	// Validate required fields
	requiredFields := []string{"default_port", "default_streams", "logfolder"}
	for _, field := range requiredFields {
		if !foundFields[field] {
			return nil, fmt.Errorf("missing required configuration field: %s", field)
		}
	}

	return config, nil
}
