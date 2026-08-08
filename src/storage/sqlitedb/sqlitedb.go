// Package sqlitedb provides the shared SQLite-open sequence used by every
// GORM-backed store in this project: create the containing directory (for
// a writable open), open via database/sql through the modernc.org/sqlite
// driver, set a busy timeout and WAL journal mode, hand the connection to
// GORM, and run AutoMigrate.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

// busyTimeoutMS is how long a connection waits on SQLITE_BUSY before giving
// up. Not exposed as an Options field -- every caller across this project
// has used the same value, and none has ever needed a different one.
const busyTimeoutMS = 5000

// Options configures Open.
type Options struct {
	// Path is the full path to the database file, e.g.
	// filepath.Join(basePath, "catalog.db"). The caller builds this --
	// Open only knows how to open a file, not where a package's database
	// belongs.
	Path string
	// ReadOnly opens the database with SQLite's mode=ro URI flag, enforced
	// by the driver. A read-only Open skips MkdirAll (a read-only opener
	// must never be the thing that creates a store's directory), the WAL
	// pragma (WAL is a database-file-level setting; a writer must set it
	// before any reader connects, see storage/catalog/db.go), and
	// AutoMigrate (the schema must already exist).
	ReadOnly bool
	// MaxConns sets the connection pool size. 0 defaults to 1 -- SQLite
	// allows only one writer at a time, so every write-capable store keeps
	// this default; a read-only pool serving genuine concurrent read
	// traffic (see storage/catalog) sets this above 1 to let WAL-mode
	// readers run concurrently.
	MaxConns int
	// Models are AutoMigrate's targets. Ignored when ReadOnly is true, or
	// when empty.
	Models []any
}

// Open opens a GORM handle to a SQLite database per opts.
func Open(opts Options) (*gorm.DB, error) {
	dsn := opts.Path + fmt.Sprintf("?_pragma=busy_timeout(%d)", busyTimeoutMS)
	if opts.ReadOnly {
		dsn = fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)", opts.Path, busyTimeoutMS)
	} else if err := os.MkdirAll(filepath.Dir(opts.Path), 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	maxConns := opts.MaxConns
	if maxConns == 0 {
		maxConns = 1
	}
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)

	if !opts.ReadOnly {
		if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("set WAL mode: %w", err)
		}
	}

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if !opts.ReadOnly && len(opts.Models) > 0 {
		if err := db.AutoMigrate(opts.Models...); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("automigrate: %w", err)
		}
	}
	return db, nil
}
