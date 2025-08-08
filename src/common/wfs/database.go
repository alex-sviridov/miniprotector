package wfs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	_ "github.com/mattn/go-sqlite3"
)

// FileMetadata represents file information stored in the database
// This extends your FileInfo with database-specific fields
type FileMetadata struct {
	ID                int64          `json:"id"`
	FileInfo          files.FileInfo `json:"file_info"`
	SourceHost        string         `json:"source_host"`
	BackupTime        time.Time      `json:"backup_time"`
	Checksum          string         `json:"checksum"`
	MetadataUpdatedAt time.Time      `json:"metadata_updated_at"`
}

// fileDB provides SQLite operations for file metadata
type fileDB struct {
	db     *sql.DB
	config *config.Config
	logger *slog.Logger
}

// newDB creates a new fileDB instance and initializes the database
func newDB(config *config.Config, logger *slog.Logger, dbPath string) (*fileDB, error) {
	// If dbpath is directory, not file, add default dbname
	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to check db path %s: %w", dbPath, err)
		}
		// Directory doesn't exist, assume dbPath is a file path and parent directories need to be created
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for db path %s: %w", dbPath, err)
		}
	} else if fileInfo.IsDir() {
		dbPath = filepath.Join(dbPath, "wfs.db")
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	fileDB := &fileDB{
		db:     db,
		config: config,
		logger: logger,
	}

	// Initialize the schema
	if err := fileDB.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return fileDB, nil
}

// initSchema creates the files table if it doesn't exist
func (fdb *fileDB) initSchema() error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL,
		name TEXT NOT NULL,
		size INTEGER NOT NULL,
		mode INTEGER NOT NULL,
		owner INTEGER NOT NULL,
		group_id INTEGER NOT NULL,
		modtime DATETIME NOT NULL,
		access_time DATETIME NOT NULL,
		ctime DATETIME NOT NULL,
		acl TEXT NOT NULL DEFAULT '{}',
		source_host TEXT NOT NULL,
		backup_time DATETIME NOT NULL,
		checksum TEXT DEFAULT '',
		metadata_updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(path, source_host, backup_time)
	);

	CREATE INDEX IF NOT EXISTS idx_path_sourcehost ON files(path, source_host);
	CREATE INDEX IF NOT EXISTS idx_path_sourcehost_modtime ON files(path, source_host, modtime);
	CREATE INDEX IF NOT EXISTS idx_checksum ON files(checksum);
	`

	_, err := fdb.db.Exec(createTableSQL)
	return err
}

// AddFile inserts a new file record into the database
func (fdb *fileDB) addFile(fileInfo *files.FileInfo, checksum string) error {
	// Serialize ACL to JSON
	aclJSON, err := json.Marshal(fileInfo.ACL)
	if err != nil {
		return fmt.Errorf("failed to serialize ACL: %w", err)
	}

	query := `
	INSERT INTO files (
		backup_time, source_host, path, name, size, mode, owner, group_id, 
		modtime, access_time, ctime, acl, checksum, metadata_updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	now := time.Now()
	result, err := fdb.db.Exec(query,
		now, fileInfo.Host, fileInfo.Path, fileInfo.Name, fileInfo.Size, fileInfo.Mode,
		fileInfo.Owner, fileInfo.Group, fileInfo.ModTime, fileInfo.AccessTime, fileInfo.CTime,
		string(aclJSON), checksum, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert file: %w", err)
	}

	_, err = result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert ID: %w", err)
	}

	return nil
}

// FileExists checks if a file with the given path exists in the database for a specific host
func (fdb *fileDB) fileExists(fileinfo *files.FileInfo) (bool, error) {
	query := `SELECT COUNT(*) FROM files WHERE source_host = ? AND path = ? AND modtime = ?`

	var count int
	err := fdb.db.QueryRow(query, fileinfo.Host, fileinfo.Path, fileinfo.ModTime).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check file existence: %w", err)
	}
	return count > 0, nil
}

// FileExistsByChecksum checks if a file with the given checksum exists in the database
func (fdb *fileDB) fileExistsByChecksum(checksum string) (bool, error) {
	if checksum == "" {
		return false, nil
	}

	query := `SELECT COUNT(*) FROM files WHERE checksum = ? AND checksum != ''`

	var count int
	err := fdb.db.QueryRow(query, checksum).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check file existence by checksum: %w", err)
	}

	return count > 0, nil
}

// GetFile retrieves the latest file metadata by path and host
func (fdb *fileDB) getFile(path, host string) (*FileMetadata, error) {
	query := `
	SELECT id, path, name, size, mode, owner, group_id, modtime, access_time, ctime, acl,
	       source_host, backup_time, checksum, metadata_updated_at
	FROM files 
	WHERE path = ? AND source_host = ?
	ORDER BY backup_time DESC
	LIMIT 1
	`

	return fdb.scanFileRow(fdb.db.QueryRow(query, path, host))
}

// GetFileByChecksum retrieves a file metadata by checksum
func (fdb *fileDB) getFileByChecksum(checksum string) (*FileMetadata, error) {
	if checksum == "" {
		return nil, nil
	}

	query := `
	SELECT id, path, name, size, mode, owner, group_id, modtime, access_time, ctime, acl,
	       source_host, backup_time, checksum, metadata_updated_at
	FROM files 
	WHERE checksum = ? AND checksum != ''
	ORDER BY backup_time DESC
	LIMIT 1
	`

	return fdb.scanFileRow(fdb.db.QueryRow(query, checksum))
}

// scanFileRow is a helper function to scan a file row
func (fdb *fileDB) scanFileRow(row *sql.Row) (*FileMetadata, error) {
	var file FileMetadata
	var aclJSON string

	err := row.Scan(
		&file.ID,
		&file.FileInfo.Path,
		&file.FileInfo.Name,
		&file.FileInfo.Size,
		&file.FileInfo.Mode,
		&file.FileInfo.Owner,
		&file.FileInfo.Group,
		&file.FileInfo.ModTime,
		&file.FileInfo.AccessTime,
		&file.FileInfo.CTime,
		&aclJSON,
		&file.SourceHost,
		&file.BackupTime,
		&file.Checksum,
		&file.MetadataUpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // File not found
		}
		return nil, fmt.Errorf("failed to scan file row: %w", err)
	}

	// Deserialize ACL from JSON
	if err := json.Unmarshal([]byte(aclJSON), &file.FileInfo.ACL); err != nil {
		return nil, fmt.Errorf("failed to deserialize ACL: %w", err)
	}

	return &file, nil
}

// Close closes the database connection
func (fdb *fileDB) close() error {
	if fdb.db != nil {
		return fdb.db.Close()
	}
	return nil
}
