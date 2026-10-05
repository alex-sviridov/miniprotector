// Package retention resolves per-file retention for one backup job: agent
// builds a Matrix of ordered rules for the job's root, brfs compiles it once
// and evaluates it per file. First matching row wins.
package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// Rule is a retention rule before resolution against a job root: Prefix is
// absolute and slash-separated.
type Rule struct {
	Prefix      string
	Include     []string
	KeepSeconds int64
}

// Row is a resolved rule. Prefix is relative to the job root, slash-separated;
// "" covers the whole root. KeepSeconds 0 means never expire.
type Row struct {
	Prefix      string   `json:"prefix"`
	Include     []string `json:"include,omitempty"`
	KeepSeconds int64    `json:"keep_seconds"`
}

// Matrix is the ordered rows for one job.
type Matrix []Row

func clean(p string) string { return path.Clean(filepath.ToSlash(p)) }

// within reports whether child equals parent or lies below it, on a path
// segment boundary ("/data2" is not within "/data").
func within(child, parent string) bool {
	return child == parent || parent == "/" || strings.HasPrefix(child, parent+"/")
}

// Build resolves rules (already in priority order) against root: rules that
// cannot overlap root are dropped, the rest rewritten root-relative, and the
// matrix ends with a default row unless an earlier row already covers
// everything unconditionally.
func Build(rules []Rule, root string, defaultKeepSeconds int64) Matrix {
	root = clean(root)
	var m Matrix
	for _, r := range rules {
		p := clean(r.Prefix)
		var rel string
		switch {
		case within(root, p):
			rel = ""
		case within(p, root):
			rel = strings.TrimPrefix(p, strings.TrimSuffix(root, "/")+"/")
		default:
			continue
		}
		m = append(m, Row{Prefix: rel, Include: r.Include, KeepSeconds: r.KeepSeconds})
		if rel == "" && len(r.Include) == 0 {
			return m
		}
	}
	return append(m, Row{KeepSeconds: defaultKeepSeconds})
}

type compiledRow struct {
	prefix      string
	prefixSlash string
	include     []string
	keep        int64
}

// Matcher is an immutable, concurrency-safe compiled Matrix.
type Matcher struct{ rows []compiledRow }

// Compile validates m and precomputes what ExpireAt needs per file.
func Compile(m Matrix) (*Matcher, error) {
	out := &Matcher{rows: make([]compiledRow, 0, len(m))}
	for i, r := range m {
		if strings.HasPrefix(r.Prefix, "/") || strings.Contains(r.Prefix, "..") {
			return nil, fmt.Errorf("retention row %d: prefix %q must be relative and free of '..'", i, r.Prefix)
		}
		if r.KeepSeconds < 0 {
			return nil, fmt.Errorf("retention row %d: negative keep_seconds", i)
		}
		for _, pat := range r.Include {
			if _, err := filepath.Match(pat, ""); err != nil {
				return nil, fmt.Errorf("retention row %d: bad include glob %q: %w", i, pat, err)
			}
		}
		out.rows = append(out.rows, compiledRow{
			prefix: r.Prefix, prefixSlash: r.Prefix + "/", include: r.Include, keep: r.KeepSeconds,
		})
	}
	return out, nil
}

// ExpireAt returns the unix-seconds expiry for relPath (root-relative,
// slash-separated, "" for the root itself): now + keep for the first matching
// row, 0 for never-expire or when no row matches.
func (m *Matcher) ExpireAt(relPath string, now time.Time) int64 {
	for i := range m.rows {
		r := &m.rows[i]
		if r.prefix != "" && relPath != r.prefix && !strings.HasPrefix(relPath, r.prefixSlash) {
			continue
		}
		if len(r.include) > 0 && !filesystem.MatchesAny(r.include, relPath) {
			continue
		}
		if r.keep == 0 {
			return 0
		}
		return now.Unix() + r.keep
	}
	return 0
}

// Rel returns p relative to root, slash-separated, "" for root itself.
func Rel(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// WriteFile atomically writes m as JSON to p, creating parent directories.
func WriteFile(p string, m Matrix) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadFile reads and compiles a matrix written by WriteFile.
func LoadFile(p string) (*Matcher, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m Matrix
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return Compile(m)
}
