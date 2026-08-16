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
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}]
	}`)

	got, err := parsePolicyFile(path, "restore")
	require.NoError(t, err)
	p, ok := got.(*RestorePolicy)
	require.True(t, ok)
	assert.Equal(t, "web01-emergency", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, []string{"web-01"}, p.ClientFilters.Hostnames)
	assert.Equal(t, "sp-1", p.StoragePolicyID)
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, p.Rules)
	assert.Equal(t, "restore", p.Kind())
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_RestorePolicyRuleHostAgnosticWhenNull(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "folder.json", `{
		"metadata": {"name": "folder"},
		"storage_policy_id": "sp-1",
		"rules": [{"host": null, "path": "/var/log", "include": true}]
	}`)

	got, err := parsePolicyFile(path, "restore")
	require.NoError(t, err)
	p := got.(*RestorePolicy)
	assert.Equal(t, "", p.Rules[0].Host, "a JSON null host decodes to the host-agnostic empty string")
}

func TestParsePolicyFile_RestoreAndBackupSameBasenameYieldDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	pathBackup := writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1"}`)
	pathRestore := writePolicyFile(t, filepath.Join(dir, "restore"), "nightly.json", `{
		"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1", "rules": [{"path": "/x", "include": true}]
	}`)

	pBackup, err := parsePolicyFile(pathBackup, "backup")
	require.NoError(t, err)
	pRestore, err := parsePolicyFile(pathRestore, "restore")
	require.NoError(t, err)

	assert.NotEqual(t, pBackup.Meta().ID, pRestore.Meta().ID, "same basename in different type subfolders must not collide")
}

func TestRestorePolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "ok"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/x", Include: true}},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ValidateMissingNameFails(t *testing.T) {
	p := &RestorePolicy{StoragePolicyID: "sp-1", Rules: []RestoreRule{{Path: "/x", Include: true}}}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptyStoragePolicyIDFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Rules:      []RestoreRule{{Path: "/x", Include: true}},
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptyRulesFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateRuleWithEmptyPathFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "", Include: true}},
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_CloneDeepCopiesRules(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true, DestPath: "/a-dest"}},
	}
	cloned := p.Clone().(*RestorePolicy)
	cloned.Rules[0].Path = "/mutated"
	cloned.Rules[0].DestPath = "/mutated-dest"
	assert.Equal(t, "/a", p.Rules[0].Path, "mutating the clone's Rules must not affect the original")
	assert.Equal(t, "/a-dest", p.Rules[0].DestPath, "mutating the clone's Rules must not affect the original")
}

func TestRestorePolicy_ToProtoSetsTypeSpecificFields(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "web01-emergency"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
			Type:          "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
	}

	pp := p.ToProto(true)

	assert.Equal(t, "r1", pp.GetId())
	assert.Equal(t, "restore", pp.GetType())
	assert.Equal(t, "sp-1", pp.GetStoragePolicyId())
	require.Len(t, pp.GetRules(), 1)
	assert.Equal(t, "web-01", pp.GetRules()[0].GetHost())
	assert.Equal(t, "/var/www/index.html", pp.GetRules()[0].GetPath())
	assert.True(t, pp.GetRules()[0].GetInclude())
	assert.Empty(t, pp.GetDestinations(), "ToProto never resolves destinations itself -- attachDestination does")
	assert.Equal(t, []string{"web-01"}, pp.GetClientFilters().GetHostnames())
}

func TestRestorePolicy_ToProtoOmitsClientFiltersWhenNotRequested(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "x"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/x", Include: true}},
	}

	pp := p.ToProto(false)

	assert.Nil(t, pp.GetClientFilters())
}

func TestRestorePolicy_ValidateDestPathOnExcludedRuleFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: false, DestPath: "/a-renamed"}},
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateDestPathEqualToPathOnExcludedRuleSucceeds(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules: []RestoreRule{
			{Path: "/a", Include: true},
			{Path: "/a/secret", Include: false, DestPath: "/a/secret"},
		},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ValidateDestPathOnIncludedRuleSucceeds(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true, DestPath: "/a-renamed"}},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ToProtoIncludesDestPath(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata: Metadata{ID: "r1", Name: "x"},
			Type:     "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true, DestPath: "/var/www/index.html.bak"}},
	}

	pp := p.ToProto(false)

	require.Len(t, pp.GetRules(), 1)
	assert.Equal(t, "/var/www/index.html.bak", pp.GetRules()[0].GetDestPath())
}

func TestRestorePolicy_Validate_RejectsNotBeforeAfterOnExcludedRule(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: false, NotBefore: 100}},
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only valid on an included rule")
}

func TestRestorePolicy_Validate_RejectsNotAfterBeforeNotBefore(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: true, NotBefore: 200, NotAfter: 100}},
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not_after")
}

func TestRestorePolicy_Validate_AcceptsUnboundedTimeframe(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: true}},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ToProto_IncludesTimeframe(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata: Metadata{ID: "r1", Name: "x"},
			Type:     "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: true, NotBefore: 100, NotAfter: 200}},
	}
	proto := p.ToProto(false)
	require.Len(t, proto.Rules, 1)
	assert.Equal(t, int64(100), proto.Rules[0].GetNotBefore())
	assert.Equal(t, int64(200), proto.Rules[0].GetNotAfter())
}

func TestRestorePolicy_ValidateAcceptsEmptyVerifyOrRestoreMode(t *testing.T) {
	for _, mode := range []string{"", "verify", "restore"} {
		p := &RestorePolicy{
			PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
			StoragePolicyID: "sp-1",
			Rules:           []RestoreRule{{Path: "/a", Include: true}},
			Mode:            mode,
		}
		assert.NoError(t, p.Validate(), "mode %q must be accepted", mode)
	}
}

func TestRestorePolicy_ValidateRejectsUnknownMode(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
		Mode:            "bogus",
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode must be 'verify' or 'restore'")
}

func TestRestorePolicy_ValidateOverwriteWithVerifyModeIsNotAnError(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
		Mode:            "verify",
		Overwrite:       true,
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ToProtoIncludesModeAndOverwrite(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata: Metadata{ID: "r1", Name: "x"},
			Type:     "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
		Mode:            "restore",
		Overwrite:       true,
	}

	pp := p.ToProto(false)

	assert.Equal(t, "restore", pp.GetMode())
	assert.True(t, pp.GetOverwrite())
}
