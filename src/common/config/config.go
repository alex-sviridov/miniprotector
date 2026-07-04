package config

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ConfigPathEnvVar is the environment variable used to override the base
// configuration directory. If unset, ResolveBaseDir defaults to the running
// binary's own directory. Both the config file (<base>/local.conf) and the
// mTLS certs directory (<base>/certs) are resolved relative to this base.
const ConfigPathEnvVar = "MP_CONFIG_PATH"

// ResolveBaseDir returns MP_CONFIG_PATH if set, otherwise the directory
// containing the running binary.
func ResolveBaseDir() (string, error) {
	if envPath := os.Getenv(ConfigPathEnvVar); envPath != "" {
		return envPath, nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine executable path: %w", err)
	}
	return filepath.Dir(exePath), nil
}

// ResolveConfigPath determines the configuration file path: <base>/local.conf,
// where base comes from ResolveBaseDir.
func ResolveConfigPath() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "local.conf"), nil
}

// ResolveCertsDir determines the mTLS certs directory: <base>/certs, where
// base comes from ResolveBaseDir. The directory is expected to contain
// ca.crt, client.crt, and client.key (see common/mtls).
func ResolveCertsDir() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "certs"), nil
}

// ResolveVarDir determines the directory for variable/runtime data (cache
// files, state files). Returns cfg.VarPath if set, otherwise the directory
// containing the running binary — the same fallback ResolveBaseDir uses,
// but resolved independently of MP_CONFIG_PATH, since variable data and
// config-file location are orthogonal concerns that happen to share a
// default.
func ResolveVarDir(cfg *Config) (string, error) {
	if cfg.VarPath != "" {
		return cfg.VarPath, nil
	}
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine executable path: %w", err)
	}
	return filepath.Dir(exePath), nil
}

// Config holds configuration from /etc/btool/local.conf
type Config struct {
	DefaultPort                int
	DefaultStreams             int
	LogFolder                  string
	ClientHashQueryBatchSize   int
	ConnectionTimeOutSec       int
	FileLockTimeoutSec         int
	StopStreamOnFileError      bool
	CAHost                     string
	JobTimeoutSec              int
	CatalogSyncBatchSize       int
	CatalogSyncPollIntervalSec int
	CatalogSyncMaxBackoffSec   int
	CatalogHost                string
	CatalogPort                int
	VarPath                    string
	ReconcileIntervalSec       int
	IssuerHost                 string
	IssuerPort                 int
	OperatingCertTTLSec        int
}

type contextKey string

const ContextKey contextKey = "config"

func GetConfigFromContext(ctx context.Context) *Config {
	config, ok := ctx.Value(ContextKey).(*Config)
	if !ok {
		return nil
	}
	return config
}

// ParseConfig reads configuration from the specified config file
// Returns error if config file doesn't exist or required fields are missing
func ParseConfig(configPath string) (*Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file %s: %w", configPath, err)
	}
	defer file.Close()

	config := &Config{
		JobTimeoutSec:              30,
		CatalogSyncBatchSize:       500,
		CatalogSyncPollIntervalSec: 5,
		CatalogSyncMaxBackoffSec:   60,
		CatalogPort:                15723,
		ReconcileIntervalSec:       30,
		IssuerPort:                 9200,
		OperatingCertTTLSec:        3600,
	}
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
		case "ca_host":
			config.CAHost = value
			foundFields["ca_host"] = true
		case "catalog_host":
			config.CatalogHost = value
			foundFields["catalog_host"] = true
		case "catalog_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid catalog_port value at line %d: %s", lineNum, value)
			}
			config.CatalogPort = port
			foundFields["catalog_port"] = true
		case "var_path":
			config.VarPath = value
			foundFields["var_path"] = true
		case "ReconcileIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid ReconcileIntervalSec value at line %d: %s", lineNum, value)
			}
			config.ReconcileIntervalSec = number
			foundFields["ReconcileIntervalSec"] = true
		case "ClientHashQueryBatchSize":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid ClientHashQueryBatchSize value at line %d: %s", lineNum, value)
			}
			config.ClientHashQueryBatchSize = number
			foundFields["ClientHashQueryBatchSize"] = true
		case "ConnectionTimeOutSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid ConnectionTimeOutSec value at line %d: %s", lineNum, value)
			}
			config.ConnectionTimeOutSec = number
			foundFields["ConnectionTimeOutSec"] = true
		case "FileLockTimeoutSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid FileLockTimeoutSec value at line %d: %s", lineNum, value)
			}
			config.FileLockTimeoutSec = number
			foundFields["FileLockTimeoutSec"] = true

		case "StopStreamOnFileError":
			config.StopStreamOnFileError = value == "true"
			foundFields["StopStreamOnFileError"] = true
		case "JobTimeoutSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid JobTimeoutSec value at line %d: %s", lineNum, value)
			}
			config.JobTimeoutSec = number
			foundFields["JobTimeoutSec"] = true
		case "CatalogSyncBatchSize":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncBatchSize value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncBatchSize = number
			foundFields["CatalogSyncBatchSize"] = true
		case "CatalogSyncPollIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncPollIntervalSec value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncPollIntervalSec = number
			foundFields["CatalogSyncPollIntervalSec"] = true
		case "CatalogSyncMaxBackoffSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncMaxBackoffSec value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncMaxBackoffSec = number
			foundFields["CatalogSyncMaxBackoffSec"] = true
		case "issuer_host":
			config.IssuerHost = value
			foundFields["issuer_host"] = true
		case "issuer_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid issuer_port value at line %d: %s", lineNum, value)
			}
			config.IssuerPort = port
			foundFields["issuer_port"] = true
		case "OperatingCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid OperatingCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.OperatingCertTTLSec = number
			foundFields["OperatingCertTTLSec"] = true
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
