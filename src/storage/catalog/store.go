package catalog

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(basePath string) (*Store, error) {
	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open catalog db: %w", err)
	}
	return &Store{db: db}, nil
}

// Entry mirrors EntryRecord's replicated fields, decoupled from the gorm
// model so callers (the gRPC server) don't need to import gorm tags.
type Entry struct {
	StoreNode      string
	JobID          string
	ObjectID       string
	Metadata       []byte
	Ctime          int64
	StoreSeq       int64
	StoreCreatedAt time.Time
	SourceHost     string
}

// EnsureEntries idempotently persists batch: a row already present for a
// given (StoreNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
	for i, e := range batch {
		records[i] = EntryRecord{
			StoreNode:      e.StoreNode,
			JobID:          e.JobID,
			ObjectID:       e.ObjectID,
			Metadata:       e.Metadata,
			Ctime:          e.Ctime,
			StoreSeq:       e.StoreSeq,
			StoreCreatedAt: e.StoreCreatedAt,
			SourceHost:     e.SourceHost,
			ReceivedAt:     now,
		}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "store_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}

// Count returns the total number of persisted entries.
func (s *Store) Count() (int64, error) {
	var n int64
	err := s.db.Model(&EntryRecord{}).Count(&n).Error
	return n, err
}

// ListEntriesFilter narrows and paginates a ListEntries query. A
// zero-valued filter matches every entry, newest first, first page.
type ListEntriesFilter struct {
	StoreNode      string    // exact match against the sending bwfs node; "" = all store nodes
	SourceHost     string    // exact match against the real originating host; "" = all source hosts
	Pattern        string    // substring match against object_id; "" = no filter
	Limit          int       // clamped to [1, 500]; 0 or negative defaults to 100
	StartingAfter  int64     // last-seen entry ID from a previous page; 0 = first page
	ReceivedAfter  time.Time // zero value = no lower bound
	ReceivedBefore time.Time // zero value = no upper bound
	SourceHosts    []string  // OR-matched; empty = no filter, additive to SourceHost
	JobNames       []string  // OR-matched against the policy name embedded in job_id
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
func (s *Store) ListEntries(filter ListEntriesFilter) ([]EntryRecord, bool, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListEntriesLimit
	}
	if limit > maxListEntriesLimit {
		limit = maxListEntriesLimit
	}

	q := s.db.Model(&EntryRecord{}).Order("id DESC")
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

// jobNamesWhere adds an OR of job_id LIKE 'backup:<name>:%' conditions, one
// per name -- job_id has no column for the policy name, so this is the
// only way to filter on it (see policyNameFromJobID in facets.go for the
// matching Go-side parse used by ListJobFacets).
func jobNamesWhere(q *gorm.DB, names []string) *gorm.DB {
	conds := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, name := range names {
		conds[i] = "job_id LIKE ?"
		args[i] = "backup:" + name + ":%"
	}
	return q.Where(strings.Join(conds, " OR "), args...)
}

// FacetFilter narrows a ListClientFacets/ListJobFacets aggregate query. A
// zero-valued filter matches every entry, no date bound.
type FacetFilter struct {
	ReceivedAfter  time.Time
	ReceivedBefore time.Time
	Pattern        string
	SourceHosts    []string // ignored by ListClientFacets -- its own dimension
	JobNames       []string // ignored by ListJobFacets -- its own dimension
}

// Facet is one aggregated row: a distinct client hostname or policy name,
// how many matching entries it has, and the most recent one.
type Facet struct {
	Name     string    `gorm:"column:name"`
	Count    int64     `gorm:"column:count"`
	LastSeen time.Time `gorm:"column:last_seen"`
}

func (f FacetFilter) applyCommon(q *gorm.DB) *gorm.DB {
	if !f.ReceivedAfter.IsZero() {
		q = q.Where("received_at >= ?", f.ReceivedAfter)
	}
	if !f.ReceivedBefore.IsZero() {
		q = q.Where("received_at <= ?", f.ReceivedBefore)
	}
	if f.Pattern != "" {
		q = q.Where("object_id LIKE ?", "%"+f.Pattern+"%")
	}
	return q
}

// ListClientFacets groups entries matching filter by source_host, dropping
// rows where source_host is empty (a decodeSourceHost failure at sync time
// -- see cmd/catalog/server.go) rather than surfacing a blank-named facet.
// filter.SourceHosts is ignored: a client facet list is never narrowed by
// its own dimension's current selection.
//
// Aggregation happens in Go, not SQL, following the same pattern as
// ListJobFacets: an earlier version used SQL-side MAX(received_at) and
// parsed the result via Go's non-portable time.Time string format, which
// crashed on any host with a negative UTC timezone offset (time.Parse
// couldn't handle the locale-dependent zone suffix). Scanning the raw,
// non-aggregated (source_host, received_at) rows and aggregating in Go
// avoids that string-parsing entirely, at the accepted cost of loading
// every matching row into memory per call rather than letting SQLite do
// the GROUP BY -- acceptable at this catalog's expected scale, and
// consistent with this package's stated preference for simple, portable
// code over premature optimization (see storage/CLAUDE.md). Revisit with
// a SQL-side strftime()-based approach if this ever becomes a measured hot
// path.
func (s *Store) ListClientFacets(filter FacetFilter) ([]Facet, error) {
	q := s.db.Model(&EntryRecord{}).
		Select("source_host, received_at").
		Where("source_host != ''")
	q = filter.applyCommon(q)
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}

	var rows []struct {
		SourceHost string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		name := r.SourceHost
		f, ok := byName[name]
		if !ok {
			f = &Facet{Name: name}
			byName[name] = f
			order = append(order, name)
		}
		f.Count++
		if r.ReceivedAt.After(f.LastSeen) {
			f.LastSeen = r.ReceivedAt
		}
	}

	facets := make([]Facet, 0, len(order))
	for _, name := range order {
		facets = append(facets, *byName[name])
	}
	return facets, nil
}

// ListJobFacets groups entries matching filter by the policy name embedded
// in job_id. Grouping happens in Go, not SQL -- job_id's colon-delimited
// format isn't fixed-width, matching this codebase's existing preference
// for decoding a similar composite ID in Go (cmd/bwfs/list.go's
// parseFileID) over a SQL substr/instr split. filter.SourceHosts is
// applied (it narrows which entries are considered); filter.JobNames is
// ignored: a job facet list is never narrowed by its own dimension's
// current selection.
func (s *Store) ListJobFacets(filter FacetFilter) ([]Facet, error) {
	q := s.db.Model(&EntryRecord{}).Select("job_id, received_at")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}

	var rows []struct {
		JobID      string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		name := policyNameFromJobID(r.JobID)
		if name == "" {
			continue
		}
		f, ok := byName[name]
		if !ok {
			f = &Facet{Name: name}
			byName[name] = f
			order = append(order, name)
		}
		f.Count++
		if r.ReceivedAt.After(f.LastSeen) {
			f.LastSeen = r.ReceivedAt
		}
	}

	facets := make([]Facet, 0, len(order))
	for _, name := range order {
		facets = append(facets, *byName[name])
	}
	return facets, nil
}

// policyNameFromJobID extracts the policy-name segment of a backup job_id
// (e.g. "nightly-db" from "backup:nightly-db:var-lib:abcd1234:..." -- see
// cmd/agent/backup.go's backupJobID). Returns "" for anything that isn't a
// "backup:"-prefixed job_id, or has fewer than two segments -- never
// errors, mirroring cmd/bwfs/list.go's parseFileID tolerance for
// malformed/foreign IDs.
func policyNameFromJobID(jobID string) string {
	parts := strings.SplitN(jobID, ":", 3)
	if len(parts) < 2 || parts[0] != "backup" {
		return ""
	}
	return parts[1]
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
