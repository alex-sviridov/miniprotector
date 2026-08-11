package catalog

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// jobNamesWhere adds an OR of job_id LIKE 'backup:<name>:%' conditions, one
// per name -- job_id has no column for the policy name, so this is the
// only way to filter on it (see policyNameFromJobID below for the matching
// Go-side parse used by ListJobFacets).
func jobNamesWhere(q *gorm.DB, names []string) *gorm.DB {
	conds := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, name := range names {
		conds[i] = "job_id LIKE ?"
		args[i] = "backup:" + name + ":%"
	}
	return q.Where(strings.Join(conds, " OR "), args...)
}

// FacetFilter narrows a ListClientFacets/ListJobFacets/ListDirectoryFacets/
// ListStoreFacets aggregate query. A zero-valued filter matches every entry,
// no date bound.
type FacetFilter struct {
	ReceivedAfter     time.Time
	ReceivedBefore    time.Time
	Pattern           string
	SourceHosts       []string // ignored by ListClientFacets -- its own dimension
	JobNames          []string // ignored by ListJobFacets -- its own dimension
	ParentDirectories []string // ignored by ListDirectoryFacets -- its own dimension
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

// Facet is one aggregated row: a distinct client hostname, policy name, or
// parent directory, how many matching entries it has, and the most recent
// one.
type Facet struct {
	Name     string    `gorm:"column:name"`
	Count    int64     `gorm:"column:count"`
	LastSeen time.Time `gorm:"column:last_seen"`
}

// facetRow is one (name, receivedAt) pair scanned from a facet query,
// before grouping.
type facetRow struct {
	Name       string
	ReceivedAt time.Time
}

// aggregateFacets groups rows by Name -- counting occurrences and tracking
// the max ReceivedAt per name, in first-seen order -- dropping rows with an
// empty Name. Shared by ListClientFacets/ListJobFacets/ListDirectoryFacets,
// which derive Name differently (raw source_host, policyNameFromJobID(job_id),
// raw parent_directory) but aggregate identically once Name is known.
//
// Aggregation happens in Go, not SQL: an earlier version used SQL-side
// MAX(received_at) and parsed the result via Go's non-portable time.Time
// string format, which crashed on any host with a negative UTC timezone
// offset (time.Parse couldn't handle the locale-dependent zone suffix).
// Scanning the raw, non-aggregated rows and aggregating here avoids that
// string-parsing entirely, at the accepted cost of loading every matching
// row into memory per call rather than letting SQLite do the GROUP BY --
// acceptable at this catalog's expected scale, and consistent with this
// package's stated preference for simple, portable code over premature
// optimization (see storage/CLAUDE.md). Revisit with a SQL-side
// strftime()-based approach if this ever becomes a measured hot path.
func aggregateFacets(rows []facetRow) []Facet {
	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		f, ok := byName[r.Name]
		if !ok {
			f = &Facet{Name: r.Name}
			byName[r.Name] = f
			order = append(order, r.Name)
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
	return facets
}

// ListClientFacets groups entries matching filter by source_host, dropping
// rows where source_host is empty (a sync-time metadata decode failure in
// SyncFileVersions, see cmd/catalog/server.go) rather than surfacing a
// blank-named facet. filter.SourceHosts is ignored: a client facet list is
// never narrowed by its own dimension's current selection.
func (s *Store) ListClientFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
		Select("source_host, received_at").
		Where("source_host != ''")
	q = filter.applyCommon(q)
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}

	var rows []struct {
		SourceHost string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: r.SourceHost, ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}

// ListJobFacets groups entries matching filter by the policy name embedded
// in job_id. Grouping happens in Go, not SQL -- job_id's colon-delimited
// format isn't fixed-width, matching this codebase's existing preference
// for decoding a similar composite ID in Go (cmd/bwfs/list.go's
// parseFileID) over a SQL substr/instr split. filter.SourceHosts is
// applied (it narrows which entries are considered); filter.JobNames is
// ignored: a job facet list is never narrowed by its own dimension's
// current selection.
func (s *Store) ListJobFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).Select("job_id, received_at")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}

	var rows []struct {
		JobID      string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: policyNameFromJobID(r.JobID), ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}

// ListDirectoryFacets groups entries matching filter by parent_directory,
// dropping rows where parent_directory is empty (a sync-time metadata
// decode failure in SyncFileVersions, see cmd/catalog/server.go) rather
// than surfacing a blank-named facet, mirroring ListClientFacets's drop of
// an empty source_host. filter.ParentDirectories is ignored: a directory
// facet list is never narrowed by its own dimension's current selection.
// Both SourceHosts and JobNames narrow it, extending the same
// "apply every other dimension, ignore your own" rule ListClientFacets/
// ListJobFacets already follow to this third dimension.
func (s *Store) ListDirectoryFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
		Select("parent_directory, received_at").
		Where("parent_directory != ''")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}

	var rows []struct {
		ParentDirectory string
		ReceivedAt      time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: r.ParentDirectory, ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}

// ListStoreFacets groups entries matching filter by store_node (the bwfs
// node that sent the batch -- exposed to API callers as "store_host"),
// dropping rows where it's empty (shouldn't happen -- StoreNode is part of
// EntryRecord's unique key -- but mirrors ListClientFacets/
// ListDirectoryFacets's defensive empty-name drop for consistency). Both
// SourceHosts and JobNames narrow it, the same "apply every other
// dimension" rule the other three facet queries already follow; there is
// no "store_hosts" field on FacetFilter to ignore for its own dimension,
// unlike the other three.
func (s *Store) ListStoreFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
		Select("store_node, received_at").
		Where("store_node != ''")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}

	var rows []struct {
		StoreNode  string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: r.StoreNode, ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
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
