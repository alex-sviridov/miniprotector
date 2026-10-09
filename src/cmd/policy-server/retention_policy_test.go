package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func validRetention() *RetentionPolicy {
	return &RetentionPolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "keep-logs"}},
		BackupType:  "filesystem",
		PathPrefix:  "/var/log",
		Include:     []string{"*.log"},
		KeepSeconds: 30 * 86400,
		Priority:    1,
	}
}

func TestParsePolicyFile_RetentionPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "keep-logs.json", `{
		"metadata": {"name": "keep-logs"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"backup_type": "filesystem",
		"path": "/var/log",
		"include": ["*.log"],
		"keep_seconds": 2592000,
		"priority": 3
	}`)

	got, err := parsePolicyFile(path, "retention")
	require.NoError(t, err)
	p, ok := got.(*RetentionPolicy)
	require.True(t, ok)
	assert.Equal(t, "keep-logs", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, "retention", p.Kind())
	assert.Equal(t, "filesystem", p.BackupType)
	assert.Equal(t, "/var/log", p.PathPrefix)
	assert.Equal(t, []string{"*.log"}, p.Include)
	assert.Equal(t, int64(2592000), p.KeepSeconds)
	assert.Equal(t, 3, p.Priority)
	assert.True(t, p.Matches("web-1", map[string]string{"env": "prod"}))
	assert.False(t, p.Matches("db-1", map[string]string{"env": "prod"}))
}

func TestRetentionPolicy_Validate(t *testing.T) {
	cases := map[string]func(p *RetentionPolicy){
		"missing name":           func(p *RetentionPolicy) { p.Metadata.Name = "" },
		"unknown backup type":    func(p *RetentionPolicy) { p.BackupType = "database" },
		"empty backup type":      func(p *RetentionPolicy) { p.BackupType = "" },
		"empty path":             func(p *RetentionPolicy) { p.PathPrefix = "" },
		"relative path":          func(p *RetentionPolicy) { p.PathPrefix = "var/log" },
		"dotdot in path":         func(p *RetentionPolicy) { p.PathPrefix = "/var/../etc" },
		"slash in include":       func(p *RetentionPolicy) { p.Include = []string{"sub/*.log"} },
		"malformed include glob": func(p *RetentionPolicy) { p.Include = []string{"[abc"} },
		"empty include pattern":  func(p *RetentionPolicy) { p.Include = []string{""} },
		"negative keep":          func(p *RetentionPolicy) { p.KeepSeconds = -1 },
		"zero priority":          func(p *RetentionPolicy) { p.Priority = 0 },
		"bad hostname pattern":   func(p *RetentionPolicy) { p.ClientFilters.Hostnames = []string{"[x"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validRetention()
			mutate(p)
			assert.Error(t, p.Validate())
		})
	}
}

func TestRetentionPolicy_ValidateAcceptsForeverAndNoInclude(t *testing.T) {
	p := validRetention()
	p.KeepSeconds = 0
	p.Include = nil
	p.PathPrefix = "/"
	assert.NoError(t, p.Validate())
}

func TestRetentionPolicy_ToProtoCarriesRuleAndHonorsClientFilters(t *testing.T) {
	p := validRetention()
	p.ClientFilters = ClientFilters{Hostnames: []string{"web-*"}}
	p.Type = "retention"

	with := p.ToProto(true)
	assert.Equal(t, "retention", with.GetType())
	r := with.GetRetention()
	require.NotNil(t, r)
	assert.Equal(t, "filesystem", r.GetBackupType())
	assert.Equal(t, "/var/log", r.GetPath())
	assert.Equal(t, []string{"*.log"}, r.GetInclude())
	assert.Equal(t, int64(30*86400), r.GetKeepSeconds())
	assert.Equal(t, int32(1), r.GetPriority())
	assert.Equal(t, []string{"web-*"}, with.GetClientFilters().GetHostnames())

	assert.Nil(t, p.ToProto(false).GetClientFilters())
}

func TestRetentionPolicy_CloneIsDeep(t *testing.T) {
	p := validRetention()
	c := p.Clone().(*RetentionPolicy)
	c.Include[0] = "changed"
	assert.Equal(t, "*.log", p.Include[0])
}

func createRetention(t *testing.T, srv *policyServerServer, name, path string) *pb.Policy {
	t.Helper()
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          name,
		Type:          "retention",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"*"}},
		Retention:     &pb.RetentionRule{BackupType: "filesystem", Path: path, KeepSeconds: 86400},
	})
	require.NoError(t, err)
	return resp
}

func TestCreatePolicy_RetentionAppendsWithIncreasingPriorityAndIgnoresRequestPriority(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	a := createRetention(t, srv, "rule-a", "/a")
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "rule-b",
		Type:          "retention",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"*"}},
		Retention:     &pb.RetentionRule{BackupType: "filesystem", Path: "/b", KeepSeconds: 1, Priority: 99},
	})
	require.NoError(t, err)

	assert.Equal(t, int32(1), a.GetRetention().GetPriority())
	assert.Equal(t, int32(2), resp.GetRetention().GetPriority(), "request priority must be ignored")
	assert.Equal(t, "retention", resp.GetType())
	_, statErr := os.Stat(filepath.Join(dir, "retention", "rule-b.json"))
	assert.NoError(t, statErr)
}

func TestCreatePolicy_RetentionValidationAndFieldMixing(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	ctx := context.Background()
	cf := &pb.ClientFilters{Hostnames: []string{"*"}}
	good := &pb.RetentionRule{BackupType: "filesystem", Path: "/a", KeepSeconds: 1}

	cases := map[string]*pb.CreatePolicyRequest{
		"missing retention":      {Name: "x", Type: "retention", ClientFilters: cf},
		"bad rule":               {Name: "x", Type: "retention", ClientFilters: cf, Retention: &pb.RetentionRule{BackupType: "filesystem", Path: "rel"}},
		"retention with backup":  {Name: "x", Type: "retention", ClientFilters: cf, Retention: good, Rpo: "24h"},
		"retention with storage": {Name: "x", Type: "retention", ClientFilters: cf, Retention: good, Port: 1},
		"retention with rules":   {Name: "x", Type: "retention", ClientFilters: cf, Retention: good, Rules: []*pb.RestoreRule{{Path: "/a", Include: true}}},
		"backup with retention":  {Name: "x", Type: "backup", ClientFilters: cf, Retention: good},
		"storage with retention": {Name: "x", Type: "storage", ClientFilters: cf, Port: 1, Config: "{}", Retention: good},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := srv.CreatePolicy(ctx, req)
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestUpdatePolicy_RetentionReplacesFieldsButKeepsPriorityAndCreatedAt(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	createRetention(t, srv, "first", "/first")
	b := createRetention(t, srv, "second", "/second")

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:            b.GetId(),
		Name:          "second-renamed",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}},
		Retention:     &pb.RetentionRule{BackupType: "filesystem", Path: "/changed", Include: []string{"*.tmp"}, KeepSeconds: 0, Priority: 50},
	})
	require.NoError(t, err)

	assert.Equal(t, "second-renamed", resp.GetName())
	assert.Equal(t, "/changed", resp.GetRetention().GetPath())
	assert.Equal(t, []string{"*.tmp"}, resp.GetRetention().GetInclude())
	assert.Equal(t, int64(0), resp.GetRetention().GetKeepSeconds())
	assert.Equal(t, int32(2), resp.GetRetention().GetPriority(), "update must not change priority")
	assert.Equal(t, b.GetCreatedAt().AsTime(), resp.GetCreatedAt().AsTime())
}

func TestUpdatePolicy_RetentionRejectsMissingRuleAndFieldMixing(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	a := createRetention(t, srv, "a", "/a")
	cf := &pb.ClientFilters{Hostnames: []string{"*"}}

	_, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: a.GetId(), Name: "a", ClientFilters: cf})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id: a.GetId(), Name: "a", ClientFilters: cf, Rpo: "1h",
		Retention: &pb.RetentionRule{BackupType: "filesystem", Path: "/a"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestDeletePolicy_RetentionRemovesFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	a := createRetention(t, srv, "gone", "/a")

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: a.GetId()})
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "retention", "gone.json"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestListPolicies_RetentionSortedByPriorityOtherTypesUnchanged(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	// priorities written directly so file order (alphabetical) != priority order
	writePolicyFile(t, filepath.Join(dir, "retention"), "a-low.json", `{"metadata":{"name":"a-low"},"client_filters":{"hostnames":["*"]},"backup_type":"filesystem","path":"/a","keep_seconds":1,"priority":3}`)
	writePolicyFile(t, filepath.Join(dir, "retention"), "b-high.json", `{"metadata":{"name":"b-high"},"client_filters":{"hostnames":["*"]},"backup_type":"filesystem","path":"/b","keep_seconds":1,"priority":1}`)
	writePolicyFile(t, filepath.Join(dir, "retention"), "c-mid.json", `{"metadata":{"name":"c-mid"},"client_filters":{"hostnames":["*"]},"backup_type":"filesystem","path":"/c","keep_seconds":1,"priority":2}`)
	require.NoError(t, srv.cache.Reload(dir, testLogger()))

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "retention"})
	require.NoError(t, err)

	var names []string
	for _, p := range resp.GetPolicies() {
		names = append(names, p.GetName())
	}
	assert.Equal(t, []string{"b-high", "c-mid", "a-low"}, names)
}
