package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	writeDB *gorm.DB
	readDB  *gorm.DB
}

func New(basePath string) (*Store, error) {
	writeDB, readDB, err := openDBs(basePath)
	if err != nil {
		return nil, err
	}
	return &Store{writeDB: writeDB, readDB: readDB}, nil
}

// Entry mirrors EntryRecord's replicated fields, decoupled from the gorm
// model so callers (the gRPC server) don't need to import gorm tags.
type Entry struct {
	StoreNode       string
	JobID           string
	ObjectID        string
	Metadata        []byte
	Ctime           int64
	StoreSeq        int64
	StoreCreatedAt  time.Time
	SourceHost      string
	ParentDirectory string
	ShortFilename   string
}

// EnsureEntries idempotently persists batch: a row already present for a
// given (StoreNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(ctx context.Context, batch []Entry) error {
	return ensureEntries(s.writeDB.WithContext(ctx), batch)
}

func ensureEntries(db *gorm.DB, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
	for i, e := range batch {
		records[i] = EntryRecord{
			StoreNode:       e.StoreNode,
			JobID:           e.JobID,
			ObjectID:        e.ObjectID,
			Metadata:        e.Metadata,
			Ctime:           e.Ctime,
			StoreSeq:        e.StoreSeq,
			StoreCreatedAt:  e.StoreCreatedAt,
			SourceHost:      e.SourceHost,
			ParentDirectory: e.ParentDirectory,
			ShortFilename:   e.ShortFilename,
			ReceivedAt:      now,
		}
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "store_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}

// DirectoryAncestor mirrors DirectoryRecord's replicated fields, decoupled
// from the gorm model the same way Entry is decoupled from EntryRecord.
type DirectoryAncestor struct {
	Path       string
	ParentPath string
	Name       string
	Depth      int
}

// EnsureDirectories idempotently persists batch: a row already present for
// a given Path is left untouched (ON CONFLICT DO NOTHING) -- directory
// structure never changes once known, and many files sync-after-sync
// share the same ancestor directories.
func (s *Store) EnsureDirectories(ctx context.Context, batch []DirectoryAncestor) error {
	return ensureDirectories(s.writeDB.WithContext(ctx), batch)
}

func ensureDirectories(db *gorm.DB, batch []DirectoryAncestor) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]DirectoryRecord, len(batch))
	for i, a := range batch {
		records[i] = DirectoryRecord{Path: a.Path, ParentPath: a.ParentPath, Name: a.Name, Depth: a.Depth}
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path"}},
		DoNothing: true,
	}).Create(&records).Error
}

// SyncBatch persists entries and their directory ancestors atomically:
// both commit, or neither does. Used by SyncFileVersions instead of
// separate EnsureEntries/EnsureDirectories calls, closing the window
// where a batch's entries could be durable with no corresponding
// directory tree.
func (s *Store) SyncBatch(ctx context.Context, entries []Entry, directories []DirectoryAncestor) error {
	return s.writeDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureEntries(tx, entries); err != nil {
			return fmt.Errorf("ensure entries: %w", err)
		}
		if err := ensureDirectories(tx, directories); err != nil {
			return fmt.Errorf("ensure directories: %w", err)
		}
		return nil
	})
}

// DirectoryChild is one directory returned by ListDirectoryChildren: a
// known subdirectory of the requested parent, plus how many files sit
// directly in it and when the most recent one arrived, both computed
// under filter's date/host/job narrowing.
type DirectoryChild struct {
	Path        string
	Name        string
	FileCount   int64
	LastSeen    time.Time
	HasChildren bool
}

// ListDirectoryChildren returns every directory whose ParentPath equals
// parentPath (parentPath == "" for the true roots: "/" and each distinct
// drive/UNC root). Existence is filter-independent -- it reflects every
// directory ever synced, not just ones with entries matching filter's
// date/host/job narrowing -- because making existence filter-aware would
// require knowing whether *any* descendant anywhere in a subtree
// matches, the same recursive-subtree question ListEntries'
// ParentDirectories filter and ListDirectoryFacets both deliberately
// avoid (exact-match only, see their comments). FileCount/LastSeen, by
// contrast, only need a direct (non-recursive) parent_directory match
// against entries, so those do respect filter -- computed as one grouped
// scan across every child, not N+1 per-child queries. HasChildren is
// true when a child itself has any row in catalog_directories (a
// DISTINCT parent_path scan), letting the UI show an expand affordance
// without a second round trip.
func (s *Store) ListDirectoryChildren(ctx context.Context, parentPath string, filter FacetFilter) ([]DirectoryChild, error) {
	var dirRows []DirectoryRecord
	if err := s.readDB.WithContext(ctx).Where("parent_path = ?", parentPath).Order("path").Find(&dirRows).Error; err != nil {
		return nil, err
	}
	if len(dirRows) == 0 {
		return []DirectoryChild{}, nil
	}

	paths := make([]string, len(dirRows))
	for i, d := range dirRows {
		paths[i] = d.Path
	}

	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
		Select("parent_directory, received_at").
		Where("parent_directory IN ?", paths)
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	var entryRows []struct {
		ParentDirectory string
		ReceivedAt      time.Time
	}
	if err := q.Scan(&entryRows).Error; err != nil {
		return nil, err
	}
	fileCount := make(map[string]int64, len(paths))
	lastSeen := make(map[string]time.Time, len(paths))
	for _, r := range entryRows {
		fileCount[r.ParentDirectory]++
		if r.ReceivedAt.After(lastSeen[r.ParentDirectory]) {
			lastSeen[r.ParentDirectory] = r.ReceivedAt
		}
	}

	var grandchildRows []struct{ ParentPath string }
	if err := s.readDB.WithContext(ctx).Model(&DirectoryRecord{}).
		Distinct("parent_path").
		Where("parent_path IN ?", paths).
		Scan(&grandchildRows).Error; err != nil {
		return nil, err
	}
	hasChildren := make(map[string]bool, len(grandchildRows))
	for _, g := range grandchildRows {
		hasChildren[g.ParentPath] = true
	}

	children := make([]DirectoryChild, len(dirRows))
	for i, d := range dirRows {
		children[i] = DirectoryChild{
			Path:        d.Path,
			Name:        d.Name,
			FileCount:   fileCount[d.Path],
			LastSeen:    lastSeen[d.Path],
			HasChildren: hasChildren[d.Path],
		}
	}
	return children, nil
}

// Count returns the total number of persisted entries.
func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.readDB.WithContext(ctx).Model(&EntryRecord{}).Count(&n).Error
	return n, err
}

// ListEntriesFilter narrows and paginates a ListEntries query. A
// zero-valued filter matches every entry, newest first, first page.
type ListEntriesFilter struct {
	StoreNode         string    // exact match against the sending bwfs node; "" = all store nodes
	SourceHost        string    // exact match against the real originating host; "" = all source hosts
	Pattern           string    // substring match against object_id; "" = no filter
	Limit             int       // clamped to [1, 500]; 0 or negative defaults to 100
	StartingAfter     int64     // last-seen entry ID from a previous page; 0 = first page
	ReceivedAfter     time.Time // zero value = no lower bound
	ReceivedBefore    time.Time // zero value = no upper bound
	SourceHosts       []string  // OR-matched; empty = no filter, additive to SourceHost
	JobNames          []string  // OR-matched against the policy name embedded in job_id
	ParentDirectories []string  // OR-matched against the exact immediate containing directory; empty = no filter
}

const (
	defaultListEntriesLimit = 100
	maxListEntriesLimit     = 500
)

// ListEntries returns entries newest-first (highest ID first), matching
// filter, plus whether more entries exist beyond the returned page.
// pattern is an unindexed SQL LIKE '%pattern%' scan against object_id
// (which already embeds the original path -- see
// workload/filesystem.FileInfo.ID) rather than decoding Metadata per row.
func (s *Store) ListEntries(ctx context.Context, filter ListEntriesFilter) ([]EntryRecord, bool, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListEntriesLimit
	}
	if limit > maxListEntriesLimit {
		limit = maxListEntriesLimit
	}

	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).Order("id DESC")
	if filter.StoreNode != "" {
		q = q.Where("store_node = ?", filter.StoreNode)
	}
	if filter.SourceHost != "" {
		q = q.Where("source_host = ?", filter.SourceHost)
	}
	if filter.Pattern != "" {
		q = q.Where("object_id LIKE ?", "%"+filter.Pattern+"%")
	}
	if filter.StartingAfter > 0 {
		q = q.Where("id < ?", filter.StartingAfter)
	}
	if !filter.ReceivedAfter.IsZero() {
		q = q.Where("received_at >= ?", filter.ReceivedAfter)
	}
	if !filter.ReceivedBefore.IsZero() {
		q = q.Where("received_at <= ?", filter.ReceivedBefore)
	}
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}

	var entries []EntryRecord
	// Fetch one extra row to detect hasMore without a separate COUNT query.
	if err := q.Limit(limit + 1).Find(&entries).Error; err != nil {
		return nil, false, err
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}

func (s *Store) Close() error {
	var errs []error
	if writeSQL, err := s.writeDB.DB(); err != nil {
		errs = append(errs, err)
	} else if err := writeSQL.Close(); err != nil {
		errs = append(errs, err)
	}
	if readSQL, err := s.readDB.DB(); err != nil {
		errs = append(errs, err)
	} else if err := readSQL.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
