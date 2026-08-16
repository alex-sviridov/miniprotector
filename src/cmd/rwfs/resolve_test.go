package main

import (
	pb "github.com/alex-sviridov/miniprotector/api"
	"testing"
)

func TestBuildRestoreFilters_OnlyIncludedRulesBecomeFilters(t *testing.T) {
	rules := []RestoreRule{
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 10, NotAfter: 20},
		{Host: "h", Path: "/etc/b", Include: false},
		{Host: "", Path: "/var", Include: true},
	}
	filters, filterToRuleIndex := buildRestoreFilters(rules)
	if len(filters) != 2 {
		t.Fatalf("expected 2 filters (excluded rule skipped), got %d", len(filters))
	}
	if filters[0].GetHost() != "h" || filters[0].GetPath() != "/etc/a" || filters[0].GetPathIsPrefix() {
		t.Fatalf("filter 0 mismatch: %+v", filters[0])
	}
	if filters[0].GetNotBefore() != 10 || filters[0].GetNotAfter() != 20 {
		t.Fatalf("filter 0 timeframe mismatch: %+v", filters[0])
	}
	if !filters[1].GetPathIsPrefix() {
		t.Fatal("host-agnostic rule must become a prefix filter")
	}
	if filterToRuleIndex[0] != 0 || filterToRuleIndex[1] != 2 {
		t.Fatalf("filterToRuleIndex mismatch: %v", filterToRuleIndex)
	}
}

func TestRestoreResolver_KeepsRowMatchingItsOwnRule(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	row := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	dispatch, ruleIndex := resolver.Feed(row, 0)
	if !dispatch {
		t.Fatal("expected the row to be kept")
	}
	if ruleIndex != 0 {
		t.Fatalf("expected the winning rule index to be 0, got %d", ruleIndex)
	}
}

func TestRestoreResolver_DropsRowShadowedByMoreSpecificRule(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true, NotBefore: 1, NotAfter: 100},      // filter 0 -- broad
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 200, NotAfter: 300}, // filter 1 -- specific
	}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/a under BOTH filters (it's under /etc, and it IS
	// /etc/a) -- each with a different version, since their windows differ.
	broadVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	specificVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 20}

	if dispatch, _ := resolver.Feed(broadVersionRow, 0); dispatch {
		t.Fatal("the broad rule's row for /etc/a must be dropped: the specific rule (index 1) governs this path")
	}
	dispatch, ruleIndex := resolver.Feed(specificVersionRow, 1)
	if !dispatch {
		t.Fatal("the specific rule's own row for its own path must be kept")
	}
	if ruleIndex != 1 {
		t.Fatalf("expected the winning rule index to be 1, got %d", ruleIndex)
	}
}

func TestRestoreResolver_DropsRowWhoseWinningRuleIsExcluded(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true},
		{Host: "h", Path: "/etc/secret", Include: false},
	}
	_, filterToRuleIndex := buildRestoreFilters(rules) // only the include rule (index 0) becomes a filter
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/secret under the broad folder filter (filter 0),
	// since the exclude rule never becomes a filter at all.
	row := &pb.FileRow{Source: "h", Path: "/etc/secret", Type: "f", Size: 10}
	if dispatch, _ := resolver.Feed(row, 0); dispatch {
		t.Fatal("the exclude rule governs /etc/secret, so this row must be dropped")
	}
}

func TestRestoreResolver_ZeroByteFileRowIsFoundButNotKept(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	zeroByte := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 0}
	if dispatch, _ := resolver.Feed(zeroByte, 0); dispatch {
		t.Fatal("a zero-byte file row must be found but not selected")
	}
	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a found-but-unselected row must not be reported as not-found, got %v", notFound)
	}
}

func TestRestoreResolver_DirectoryRowIsDispatched(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	dir := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "d"}
	dispatch, ruleIndex := resolver.Feed(dir, 0)
	if !dispatch {
		t.Fatal("a directory row must now be dispatched, not dropped")
	}
	if ruleIndex != 0 {
		t.Fatalf("expected the winning rule index to be 0, got %d", ruleIndex)
	}

	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a dispatched directory must not be reported as not-found, got %v", notFound)
	}
}

// A bounded window that matched nothing gets the distinguished reason:
// the file may well exist on the store, just not inside the window, which
// is a diagnosably different problem from a typo'd path.
func TestRestoreResolver_NotFound_FileLevelFilterWithNoKeptRowIsAFailure(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/missing", Include: true, NotBefore: 100, NotAfter: 200}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)
	// No Feed calls at all -- bwfs never resolved anything for filter 0.

	notFound := resolver.NotFound()
	if len(notFound) != 1 {
		t.Fatalf("expected exactly one not-found entry, got %v", notFound)
	}
	if notFound[0].Host != "h" || notFound[0].Path != "/etc/missing" {
		t.Fatalf("not-found entry mismatch: %+v", notFound[0])
	}
	if notFound[0].Reason != "no version in timeframe" {
		t.Fatalf("expected the distinguished reason, got %q", notFound[0].Reason)
	}
}

// The other half of that discriminator: with no timeframe requested at
// all, the query window covered all of history, so zero rows means the
// file genuinely isn't on this store -- and saying "no version in
// timeframe" there would misdirect the operator toward a window they never
// set. Only one side may be set for the window to count as bounded.
func TestRestoreResolver_NotFound_FileLevelFilterWithNoTimeframeAndNoKeptRowUsesGenericReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rule       RestoreRule
		wantReason string
	}{
		{"unbounded", RestoreRule{Host: "h", Path: "/etc/missing", Include: true}, "not found on this store"},
		{"lower bound only", RestoreRule{Host: "h", Path: "/etc/missing", Include: true, NotBefore: 100}, "no version in timeframe"},
		{"upper bound only", RestoreRule{Host: "h", Path: "/etc/missing", Include: true, NotAfter: 200}, "no version in timeframe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []RestoreRule{tc.rule}
			_, filterToRuleIndex := buildRestoreFilters(rules)
			resolver := newRestoreResolver(rules, filterToRuleIndex)

			notFound := resolver.NotFound()
			if len(notFound) != 1 {
				t.Fatalf("expected exactly one not-found entry, got %v", notFound)
			}
			if notFound[0].Reason != tc.wantReason {
				t.Fatalf("expected reason %q, got %q", tc.wantReason, notFound[0].Reason)
			}
		})
	}
}

func TestRestoreResolver_NotFound_FolderLevelFilterWithNoKeptRowIsNotAFailure(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/empty", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a folder rule matching nothing is a legitimate empty result, got %v", notFound)
	}
}
