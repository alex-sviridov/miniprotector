package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveRestoreFile_NoRulesIsUnselected(t *testing.T) {
	if resolveRestoreFile(nil, "web-01", "/var/log/x") {
		t.Fatal("expected unselected with no rules")
	}
}

func TestResolveRestoreFile_HostAgnosticFolderRuleSelectsDescendant(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/var/log", Include: true}}
	if !resolveRestoreFile(rules, "any-host", "/var/log/nested/x.log") {
		t.Fatal("expected selected: host-agnostic folder rule covers any host")
	}
}

func TestResolveRestoreFile_HostAgnosticFolderRuleDoesNotOverMatchSiblingPath(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/var/log", Include: true}}
	if resolveRestoreFile(rules, "any-host", "/var/log2/x.log") {
		t.Fatal("/var/log2 is not a descendant of /var/log despite the string prefix match")
	}
}

func TestResolveRestoreFile_ExactHostSpecificRuleWinsOverFolderRule(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/var/log", Include: true},
		{Host: "web-01", Path: "/var/log/app.log", Include: false},
	}
	if resolveRestoreFile(rules, "web-01", "/var/log/app.log") {
		t.Fatal("exact file-level exclude must win over the folder-level include")
	}
	if !resolveRestoreFile(rules, "web-02", "/var/log/app.log") {
		t.Fatal("the exclude is scoped to web-01 only; web-02's copy stays included")
	}
}

func TestResolveRestoreFile_LongestMatchingAncestorWins(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/var", Include: true},
		{Host: "", Path: "/var/log", Include: false},
	}
	if resolveRestoreFile(rules, "any-host", "/var/log/x") {
		t.Fatal("the more specific /var/log exclude must win over the /var include")
	}
	if !resolveRestoreFile(rules, "any-host", "/var/lib/x") {
		t.Fatal("/var/lib isn't covered by the /var/log exclude, so the /var include applies")
	}
}

func TestResolveRestoreFileRule_ReturnsWinningRuleIndex(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/var", Include: true},
		{Host: "", Path: "/var/log", Include: false},
	}
	idx, include, found := resolveRestoreFileRule(rules, "any-host", "/var/log/x")
	assert.True(t, found)
	assert.False(t, include)
	assert.Equal(t, 1, idx, "the more specific /var/log exclude (index 1) must be reported as the winner")

	idx, include, found = resolveRestoreFileRule(rules, "any-host", "/var/lib/x")
	assert.True(t, found)
	assert.True(t, include)
	assert.Equal(t, 0, idx, "only the /var include (index 0) covers /var/lib")
}

func TestResolveRestoreFileRule_NoMatchReportsNotFound(t *testing.T) {
	_, _, found := resolveRestoreFileRule(nil, "host", "/x")
	assert.False(t, found)
}

func TestResolveRestoreFileRule_ExactRuleWinsOverFolderRuleRegardlessOfIndex(t *testing.T) {
	rules := []RestoreRule{
		{Host: "web-01", Path: "/var/log/app.log", Include: false}, // index 0
		{Host: "", Path: "/var/log", Include: true},                // index 1
	}
	idx, include, found := resolveRestoreFileRule(rules, "web-01", "/var/log/app.log")
	assert.True(t, found)
	assert.False(t, include)
	assert.Equal(t, 0, idx)
}
