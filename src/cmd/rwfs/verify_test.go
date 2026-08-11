package main

import (
	"strings"
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRulesStdin_ParsesRules(t *testing.T) {
	rules, err := parseRulesStdin(strings.NewReader(`{"rules":[{"host":"web-01","path":"/a.txt","include":true}]}`))
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "web-01", rules[0].Host)
	assert.Equal(t, "/a.txt", rules[0].Path)
	assert.True(t, rules[0].Include)
}

// An empty rule set must be an error, not a vacuous success: it selects
// nothing, so "verified 0, warnings 0" would look like a real pass and a
// one-shot caller would never run it again.
func TestParseRulesStdin_EmptyRuleSetIsAnError(t *testing.T) {
	for name, payload := range map[string]string{
		"empty array":  `{"rules":[]}`,
		"null rules":   `{"rules":null}`,
		"empty object": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseRulesStdin(strings.NewReader(payload))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "at least one rule")
		})
	}
}

func TestParseRulesStdin_MalformedJSONIsAnError(t *testing.T) {
	_, err := parseRulesStdin(strings.NewReader(`{"rules":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse rules from stdin")
}

func TestApplyRulesStdin_FileLevelRuleWithNoMatchingRowFails(t *testing.T) {
	rows := []*pb.FileRow{
		{Source: "web-01", Path: "/var/www/index.html", Type: "f", Size: 10, FileUuid: "u1"},
	}
	rules := []RestoreRule{
		{Host: "web-01", Path: "/var/www/index.html", Include: true},
		{Host: "web-01", Path: "/missing.txt", Include: true},
	}
	selected, notFound := applyRulesStdin(rows, rules)
	require.Len(t, selected, 1)
	assert.Equal(t, "u1", selected[0].FileUuid)
	require.Len(t, notFound, 1)
	assert.Equal(t, "/missing.txt", notFound[0].Path)
}

func TestApplyRulesStdin_FolderLevelRuleWithNoMatchingRowIsNotAFailure(t *testing.T) {
	rows := []*pb.FileRow{}
	rules := []RestoreRule{{Host: "", Path: "/empty/folder", Include: true}}
	selected, notFound := applyRulesStdin(rows, rules)
	assert.Empty(t, selected)
	assert.Empty(t, notFound, "a folder rule matching nothing is not itself a failure")
}

// A zero-byte file can't be chunk-verified (nothing to checksum), so it is
// correctly absent from selected -- but it was really backed up and really
// is on this store, so a file-level rule naming it must NOT be reported as
// "not found on this store", which would fail the whole one-shot
// verification task forever.
func TestApplyRulesStdin_ZeroByteFileIsFoundButNotSelected(t *testing.T) {
	rows := []*pb.FileRow{
		{Source: "web-01", Path: "/var/www/empty.txt", Type: "f", Size: 0, FileUuid: "u3"},
	}
	rules := []RestoreRule{{Host: "web-01", Path: "/var/www/empty.txt", Include: true}}
	selected, notFound := applyRulesStdin(rows, rules)
	assert.Empty(t, selected, "a zero-byte file has no chunks to verify")
	assert.Empty(t, notFound, "a real zero-byte file must not be reported as missing from this store")
}

// Same guarantee for a directory row: present on the store, nothing to
// checksum, must not be misreported as missing.
func TestApplyRulesStdin_DirectoryRowIsFoundButNotSelected(t *testing.T) {
	rows := []*pb.FileRow{
		{Source: "web-01", Path: "/var/www", Type: "d", Size: 0, FileUuid: "u4"},
	}
	rules := []RestoreRule{{Host: "web-01", Path: "/var/www", Include: true}}
	selected, notFound := applyRulesStdin(rows, rules)
	assert.Empty(t, selected)
	assert.Empty(t, notFound)
}

func TestApplyRulesStdin_ExcludedRowIsNotSelected(t *testing.T) {
	rows := []*pb.FileRow{{Source: "web-01", Path: "/var/log/app.log", Type: "f", Size: 5, FileUuid: "u2"}}
	rules := []RestoreRule{
		{Host: "", Path: "/var/log", Include: true},
		{Host: "web-01", Path: "/var/log/app.log", Include: false},
	}
	selected, notFound := applyRulesStdin(rows, rules)
	assert.Empty(t, selected)
	assert.Empty(t, notFound)
}
