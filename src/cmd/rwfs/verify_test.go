package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
