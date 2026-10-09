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

// ResolvePoliciesDir determines the policy-server policy directory:
// <base>/policies, where base comes from ResolveBaseDir.
func ResolvePoliciesDir() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "policies"), nil
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
	DefaultPort                      int
	DefaultStreams                   int
	DefaultWindow                    int // brfs: max in-flight chunks per stream (--window default)
	GrpcWindowBytes                  int // fixed gRPC flow-control window in bytes; 0 = grpc-go's dynamic default
	LogDir                           string
	ClientHashQueryBatchSize         int
	ConnectionTimeOutSec             int
	FileLockTimeoutSec               int
	StopStreamOnFileError            bool
	CAHost                           string
	JobTimeoutSec                    int
	CatalogSyncBatchSize             int
	CatalogSyncPollIntervalSec       int
	CatalogSyncMaxBackoffSec         int
	CatalogSyncDamageIntervalSec     int // catalogsync: seconds between damaged-set snapshots sent to the catalog
	CatalogHost                      string
	CatalogPort                      int
	VarPath                          string
	ReconcileIntervalSec             int
	IssuerHost                       string
	IssuerPort                       int
	ClientManagerAPIPort             int
	OperatingCertTTLSec              int
	BootstrapCertRefreshIntervalSec  int
	BootstrapCertTTLSec              int
	OperatingCertFetchIntervalSec    int
	IssuerSelfCertTTLSec             int
	IssuerSelfCertRefreshIntervalSec int
	PolicyServerHost                 string
	PolicyServerPort                 int
	PolicyFetchIntervalSec           int
	BackupWindowGraceSec             int
	RetentionDefaultDays             int
	StoreCleanupIntervalSec          int
	StoreVacuumIntervalSec           int
	StoreGCBatchSize                 int
	StoreCleanupDryRun               bool
	StoreIncompleteFileDataGraceSec  int
	StoreDeletionLogRetentionSec     int
	MaxConcurrentBackupJobs          int
	LogGatewayHost                   string
	LogGatewayPort                   int
	ClientManagerAPIHost             string
	APIServerPort                    int
	APIServerToken                   string
	APIServerJobStatusPort           int
	ClientManagerAdminAPIPort        int
	ClientManagerAdminAPIHost        string
	AdhocPolicyTimeoutSec            int
	CheckinRetentionSec              int
	APIServerHost                    string
	RestoreCleanupIntervalSec        int
	RestoreCleanupGracePeriodSec     int
	RwfsRetries                      int
	RestoreCommitFiles               int
	RestoreCommitBytes               int64
}

// Bounds for grpc_window_bytes: HTTP/2's initial window may not go below 64KiB
// (grpc-go ignores smaller values) and is capped well under its 2GiB limit.
const (
	MinGrpcWindowBytes = 64 * 1024
	MaxGrpcWindowBytes = 1 << 30
)

// MaxRestoreCommitFiles caps restore_commit_files: every pending file holds
// its descriptor open until the checkpoint.
const MaxRestoreCommitFiles = 1024

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
		JobTimeoutSec:                    30,
		CatalogSyncBatchSize:             500,
		CatalogSyncPollIntervalSec:       5,
		CatalogSyncMaxBackoffSec:         60,
		CatalogSyncDamageIntervalSec:     60,
		CatalogPort:                      15723,
		ReconcileIntervalSec:             30,
		IssuerPort:                       9200,
		ClientManagerAPIPort:             9500,
		APIServerPort:                    8090,
		APIServerJobStatusPort:           8091,
		OperatingCertTTLSec:              3600,
		BootstrapCertRefreshIntervalSec:  86400,
		BootstrapCertTTLSec:              7776000,
		OperatingCertFetchIntervalSec:    900,
		IssuerSelfCertTTLSec:             7776000,
		IssuerSelfCertRefreshIntervalSec: 86400,
		PolicyServerPort:                 9300,
		PolicyFetchIntervalSec:           900,
		BackupWindowGraceSec:             3600,
		RetentionDefaultDays:             7,
		StoreCleanupIntervalSec:          3600,
		StoreVacuumIntervalSec:           86400,
		StoreGCBatchSize:                 500,
		StoreIncompleteFileDataGraceSec:  86400,
		StoreDeletionLogRetentionSec:     2592000,
		MaxConcurrentBackupJobs:          2,
		LogGatewayPort:                   9400,
		ClientManagerAdminAPIPort:        9501,
		ConnectionTimeOutSec:             30,
		AdhocPolicyTimeoutSec:            3600,
		CheckinRetentionSec:              86400,
		RestoreCleanupIntervalSec:        300,
		RestoreCleanupGracePeriodSec:     900,
		RwfsRetries:                      3,
		RestoreCommitFiles:               64,
		RestoreCommitBytes:               64 << 20,
		DefaultWindow:                    16,
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
		case "default_window":
			window, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid default_window value at line %d: %s", lineNum, value)
			}
			if window <= 0 {
				return nil, fmt.Errorf("default_window must be positive at line %d: %s", lineNum, value)
			}
			config.DefaultWindow = window
			foundFields["default_window"] = true
		case "grpc_window_bytes":
			bytes, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid grpc_window_bytes value at line %d: %s", lineNum, value)
			}
			if bytes != 0 && (bytes < MinGrpcWindowBytes || bytes > MaxGrpcWindowBytes) {
				return nil, fmt.Errorf("grpc_window_bytes must be 0 or between %d and %d at line %d: %s", MinGrpcWindowBytes, MaxGrpcWindowBytes, lineNum, value)
			}
			config.GrpcWindowBytes = bytes
			foundFields["grpc_window_bytes"] = true
		case "log_dir":
			config.LogDir = value
			foundFields["log_dir"] = true
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
		case "CatalogSyncDamageIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncDamageIntervalSec value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncDamageIntervalSec = number
			foundFields["CatalogSyncDamageIntervalSec"] = true
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
		case "clientmanager_api_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid clientmanager_api_port value at line %d: %s", lineNum, value)
			}
			config.ClientManagerAPIPort = port
			foundFields["clientmanager_api_port"] = true
		case "OperatingCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid OperatingCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.OperatingCertTTLSec = number
			foundFields["OperatingCertTTLSec"] = true
		case "BootstrapCertRefreshIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid BootstrapCertRefreshIntervalSec value at line %d: %s", lineNum, value)
			}
			config.BootstrapCertRefreshIntervalSec = number
			foundFields["BootstrapCertRefreshIntervalSec"] = true
		case "BootstrapCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid BootstrapCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.BootstrapCertTTLSec = number
			foundFields["BootstrapCertTTLSec"] = true
		case "OperatingCertFetchIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid OperatingCertFetchIntervalSec value at line %d: %s", lineNum, value)
			}
			config.OperatingCertFetchIntervalSec = number
			foundFields["OperatingCertFetchIntervalSec"] = true
		case "IssuerSelfCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid IssuerSelfCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.IssuerSelfCertTTLSec = number
			foundFields["IssuerSelfCertTTLSec"] = true
		case "IssuerSelfCertRefreshIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid IssuerSelfCertRefreshIntervalSec value at line %d: %s", lineNum, value)
			}
			config.IssuerSelfCertRefreshIntervalSec = number
			foundFields["IssuerSelfCertRefreshIntervalSec"] = true
		case "policy_server_host":
			config.PolicyServerHost = value
			foundFields["policy_server_host"] = true
		case "policy_server_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid policy_server_port value at line %d: %s", lineNum, value)
			}
			config.PolicyServerPort = port
			foundFields["policy_server_port"] = true
		case "log_gateway_host":
			config.LogGatewayHost = value
			foundFields["log_gateway_host"] = true
		case "log_gateway_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid log_gateway_port value at line %d: %s", lineNum, value)
			}
			config.LogGatewayPort = port
			foundFields["log_gateway_port"] = true
		case "clientmanager_api_host":
			config.ClientManagerAPIHost = value
			foundFields["clientmanager_api_host"] = true
		case "clientmanager_admin_api_host":
			config.ClientManagerAdminAPIHost = value
			foundFields["clientmanager_admin_api_host"] = true
		case "clientmanager_admin_api_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid clientmanager_admin_api_port value at line %d: %s", lineNum, value)
			}
			config.ClientManagerAdminAPIPort = port
			foundFields["clientmanager_admin_api_port"] = true
		case "api_server_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid api_server_port value at line %d: %s", lineNum, value)
			}
			config.APIServerPort = port
			foundFields["api_server_port"] = true
		case "api_server_token":
			config.APIServerToken = value
			foundFields["api_server_token"] = true
		case "APIServerJobStatusPort":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid APIServerJobStatusPort value at line %d: %s", lineNum, value)
			}
			config.APIServerJobStatusPort = number
			foundFields["APIServerJobStatusPort"] = true
		case "PolicyFetchIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid PolicyFetchIntervalSec value at line %d: %s", lineNum, value)
			}
			config.PolicyFetchIntervalSec = number
			foundFields["PolicyFetchIntervalSec"] = true
		case "BackupWindowGraceSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid BackupWindowGraceSec value at line %d: %s", lineNum, value)
			}
			config.BackupWindowGraceSec = number
			foundFields["BackupWindowGraceSec"] = true
		case "RetentionDefaultDays":
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("invalid RetentionDefaultDays value at line %d: %s", lineNum, value)
			}
			config.RetentionDefaultDays = number
			foundFields["RetentionDefaultDays"] = true
		case "StoreCleanupIntervalSec", "StoreVacuumIntervalSec", "StoreIncompleteFileDataGraceSec", "StoreDeletionLogRetentionSec":
			// 0 is meaningful for the two intervals (disables that loop) and
			// harmless for the other two; only negatives are invalid.
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("invalid %s value at line %d: %s", key, lineNum, value)
			}
			switch key {
			case "StoreCleanupIntervalSec":
				config.StoreCleanupIntervalSec = number
			case "StoreVacuumIntervalSec":
				config.StoreVacuumIntervalSec = number
			case "StoreIncompleteFileDataGraceSec":
				config.StoreIncompleteFileDataGraceSec = number
			case "StoreDeletionLogRetentionSec":
				config.StoreDeletionLogRetentionSec = number
			}
			foundFields[key] = true
		case "StoreGCBatchSize":
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 {
				return nil, fmt.Errorf("invalid StoreGCBatchSize value at line %d: %s", lineNum, value)
			}
			config.StoreGCBatchSize = number
			foundFields["StoreGCBatchSize"] = true
		case "StoreCleanupDryRun":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("invalid StoreCleanupDryRun value at line %d: %s", lineNum, value)
			}
			config.StoreCleanupDryRun = b
			foundFields["StoreCleanupDryRun"] = true
		case "MaxConcurrentBackupJobs":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid MaxConcurrentBackupJobs value at line %d: %s", lineNum, value)
			}
			config.MaxConcurrentBackupJobs = number
			foundFields["MaxConcurrentBackupJobs"] = true
		case "AdhocPolicyTimeoutSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid AdhocPolicyTimeoutSec value at line %d: %s", lineNum, value)
			}
			if number <= 0 {
				return nil, fmt.Errorf("AdhocPolicyTimeoutSec must be positive at line %d: %s", lineNum, value)
			}
			config.AdhocPolicyTimeoutSec = number
			foundFields["AdhocPolicyTimeoutSec"] = true
		case "CheckinRetentionSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CheckinRetentionSec value at line %d: %s", lineNum, value)
			}
			if number <= 0 {
				return nil, fmt.Errorf("CheckinRetentionSec must be positive at line %d: %s", lineNum, value)
			}
			config.CheckinRetentionSec = number
			foundFields["CheckinRetentionSec"] = true
		case "api_server_host":
			config.APIServerHost = value
			foundFields["api_server_host"] = true
		case "RestoreCleanupIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid RestoreCleanupIntervalSec value at line %d: %s", lineNum, value)
			}
			config.RestoreCleanupIntervalSec = number
			foundFields["RestoreCleanupIntervalSec"] = true
		case "RestoreCleanupGracePeriodSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid RestoreCleanupGracePeriodSec value at line %d: %s", lineNum, value)
			}
			config.RestoreCleanupGracePeriodSec = number
			foundFields["RestoreCleanupGracePeriodSec"] = true
		case "RwfsRetries":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid RwfsRetries value at line %d: %s", lineNum, value)
			}
			if number <= 0 {
				return nil, fmt.Errorf("RwfsRetries must be positive at line %d: %s", lineNum, value)
			}
			config.RwfsRetries = number
			foundFields["RwfsRetries"] = true
		case "restore_commit_files":
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 || number > MaxRestoreCommitFiles {
				return nil, fmt.Errorf("invalid restore_commit_files value at line %d: %s (must be 0-%d)", lineNum, value, MaxRestoreCommitFiles)
			}
			config.RestoreCommitFiles = number
		case "restore_commit_bytes":
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("invalid restore_commit_bytes value at line %d: %s (must be >= 0)", lineNum, value)
			}
			config.RestoreCommitBytes = number
		default:
			return nil, fmt.Errorf("unknown configuration key at line %d: %s", lineNum, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	// Validate required fields
	requiredFields := []string{"default_port", "default_streams", "log_dir"}
	for _, field := range requiredFields {
		if !foundFields[field] {
			return nil, fmt.Errorf("missing required configuration field: %s", field)
		}
	}

	return config, nil
}
