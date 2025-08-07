package wfs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alex-sviridov/miniprotector/common/files"
	_ "github.com/mattn/go-sqlite3"
)

// FileMetadata represents file information stored in the database
// This extends your FileInfo with database-specific fields
type FileMetadata struct {
	ID         int64          `json:"id"`
	FileInfo   files.FileInfo `json:"file_info"`
	SourceHost string         `json:"source_host"`
	BackupTime time.Time      `json:"backup_time"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// FileDB provides SQLite operations for file metadata
type FileDB struct {
	db *sql.DB
}

// NewFileDB creates a new FileDB instance and initializes the database
func NewFileDB(dbPath string) (*FileDB, error) {
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

	fileDB := &FileDB{db: db}

	// Initialize the schema
	if err := fileDB.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return fileDB, nil
}

// initSchema creates the files table if it doesn't exist
func (fdb *FileDB) initSchema() error {
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(path, source_host, backup_time)
	);

	CREATE INDEX IF NOT EXISTS idx_path_sourcehost ON files(path, source_host);
	CREATE INDEX IF NOT EXISTS idx_path_sourcehost_modtime ON files(path, source_host, modtime);
	`

	_, err := fdb.db.Exec(createTableSQL)
	return err
}

// AddFile inserts a new file record into the database
func (fdb *FileDB) AddFile(fileInfo files.FileInfo, host string, backupTime time.Time) (*FileMetadata, error) {
	// Serialize ACL to JSON
	aclJSON, err := json.Marshal(fileInfo.ACL)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize ACL: %w", err)
	}

	query := `
	INSERT INTO files (
		backup_time, source_host, path, name, size, mode, owner, group_id, 
		modtime, access_time, ctime, acl, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	now := time.Now()
	result, err := fdb.db.Exec(query,
		backupTime, host, fileInfo.Path, fileInfo.Name, fileInfo.Size, fileInfo.Mode,
		fileInfo.Owner, fileInfo.Group, fileInfo.ModTime, fileInfo.AccessTime, fileInfo.CTime,
		string(aclJSON), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert file: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get last insert ID: %w", err)
	}

	return &FileMetadata{
		ID:         id,
		FileInfo:   fileInfo,
		SourceHost: host,
		BackupTime: backupTime,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// FileExists checks if a file with the given path exists in the database for a specific host
func (fdb *FileDB) FileExists(path, host string, modtime time.Time, ctime time.Time) (bool, error) {
    query := `SELECT ctime FROM files WHERE source_host = ? AND path = ? AND modtime = ?`

    var ctimeDb time.Time
    err := fdb.db.QueryRow(query, host, path, modtime).Scan(&ctimeDb)
    if err != nil {
        if err == sql.ErrNoRows {
            // File doesn't exist - this is not an error, just return false
            return false, nil
        }
        // Actual error occurred
        return false, fmt.Errorf("failed to check file existence: %w", err)
    }

    // File exists, return true
    return true, nil
}

// GetFile retrieves the latest file metadata by path and host
func (fdb *FileDB) GetFile(path, host string) (*FileMetadata, error) {
	query := `
	SELECT id, path, name, size, mode, owner, group_id, modtime, access_time, ctime, acl,
	       source_host, backup_time, hash, created_at, updated_at
	FROM files 
	WHERE path = ? AND source_host = ?
	ORDER BY backup_time DESC
	LIMIT 1
	`

	return fdb.scanFileRow(fdb.db.QueryRow(query, path, host))
}

// scanFileRow is a helper function to scan a file row
func (fdb *FileDB) scanFileRow(row *sql.Row) (*FileMetadata, error) {
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
		&file.CreatedAt,
		&file.UpdatedAt,
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

// UpdateFile updates an existing file's metadata
func (fdb *FileDB) UpdateFile(path, host string, backupTime time.Time, fileInfo files.FileInfo, hash string) error {
	// Serialize ACL to JSON
	aclJSON, err := json.Marshal(fileInfo.ACL)
	if err != nil {
		return fmt.Errorf("failed to serialize ACL: %w", err)
	}

	query := `
	UPDATE files 
	SET name = ?, size = ?, mode = ?, owner = ?, group_id = ?, 
	    modtime = ?, access_time = ?, ctime = ?, acl = ?, hash = ?, updated_at = ?
	WHERE path = ? AND source_host = ? AND backup_time = ?
	`

	result, err := fdb.db.Exec(query,
		fileInfo.Name, fileInfo.Size, fileInfo.Mode, fileInfo.Owner, fileInfo.Group,
		fileInfo.ModTime, fileInfo.AccessTime, fileInfo.CTime, string(aclJSON), hash, time.Now(),
		path, host, backupTime,
	)
	if err != nil {
		return fmt.Errorf("failed to update file: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("file not found: %s on host %s at %v", path, host, backupTime)
	}

	return nil
}
