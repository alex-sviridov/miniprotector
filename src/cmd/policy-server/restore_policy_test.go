package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyFile_RestorePolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "web01-emergency.json", `{
		"metadata": {"name": "web01-emergency"},
		"client_filters": {"hostnames": ["web-01"]},
		"source_store": "bwfs-east.internal:8080",
		"config": {"files": ["/var/www/index.html"]}
	}`)

	got, err := parsePolicyFile(path, "restore")
	require.NoError(t, err)
	p, ok := got.(*RestorePolicy)
	require.True(t, ok)
	assert.Equal(t, "web01-emergency", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, []string{"web-01"}, p.ClientFilters.Hostnames)
	assert.Equal(t, "bwfs-east.internal:8080", p.SourceStore)
	assert.JSONEq(t, `{"files": ["/var/www/index.html"]}`, string(p.Config))
	assert.Equal(t, "restore", p.Kind())
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_RestoreAndBackupSameBasenameYieldDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	pathBackup := writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1"}`)
	pathRestore := writePolicyFile(t, filepath.Join(dir, "restore"), "nightly.json", `{
		"metadata": {"name": "nightly"}, "source_store": "bwfs:8080", "config": {}
	}`)

	pBackup, err := parsePolicyFile(pathBackup, "backup")
	require.NoError(t, err)
	pRestore, err := parsePolicyFile(pathRestore, "restore")
	require.NoError(t, err)

	assert.NotEqual(t, pBackup.Meta().ID, pRestore.Meta().ID, "same basename in different type subfolders must not collide")
}

func TestRestorePolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "ok"}},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{"files": []}`),
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ValidateMissingNameFails(t *testing.T) {
	p := &RestorePolicy{SourceStore: "bwfs:8080", Config: []byte(`{}`)}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptySourceStoreFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateSourceStoreMissingPortFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs-no-port",
		Config:      []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptyConfigFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs:8080",
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateMalformedConfigJSONFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs:8080",
		Config:      []byte(`not json`),
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_CloneDeepCopiesConfig(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{"a":1}`),
	}
	cloned := p.Clone().(*RestorePolicy)
	cloned.Config[2] = 'X'
	assert.Equal(t, `{"a":1}`, string(p.Config), "mutating the clone's Config must not affect the original")
}

func TestRestorePolicy_ToProtoSetsTypeSpecificFields(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "web01-emergency"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
			Type:          "restore",
		},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{"files":[]}`),
	}

	pp := p.ToProto(true)

	assert.Equal(t, "r1", pp.GetId())
	assert.Equal(t, "restore", pp.GetType())
	assert.Equal(t, "bwfs:8080", pp.GetSourceStore())
	assert.JSONEq(t, `{"files":[]}`, pp.GetConfig())
	assert.Equal(t, []string{"web-01"}, pp.GetClientFilters().GetHostnames())
}

func TestRestorePolicy_ToProtoOmitsClientFiltersWhenNotRequested(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "x"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
		},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{}`),
	}

	pp := p.ToProto(false)

	assert.Nil(t, pp.GetClientFilters())
}
