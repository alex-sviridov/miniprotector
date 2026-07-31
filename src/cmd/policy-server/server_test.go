package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// attributeExtensionOID mirrors cmd/issuer/e2e_test.go's own copy -- the
// same private-use OID issuer embeds attributes under; small OID constants
// like this are duplicated per test file in this codebase rather than
// exported from common/mtls.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// peerCertContext builds a context carrying only a verified mTLS peer
// certificate (with attrs as its embedded extension) for hostname, with no
// gRPC metadata attached. fakeAuthContext (below) layers job-id metadata
// on top for the common case; TestGetPolicies_MissingJobIDRejected uses
// this directly to exercise the "no job-id metadata at all" path.
func peerCertContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	var extensions []pkix.Extension
	if attrs != nil {
		value, err := json.Marshal(attrs)
		require.NoError(t, err)
		extensions = []pkix.Extension{{Id: attributeExtensionOID, Critical: false, Value: value}}
	}

	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1),
		Subject:         pkix.Name{CommonName: hostname},
		DNSNames:        []string{hostname},
		NotBefore:       time.Now(),
		NotAfter:        time.Now().Add(time.Hour),
		ExtraExtensions: extensions,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name, plus job-id metadata every GetPolicies test needs by default now
// that it's required.
func fakeAuthContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(peerCertContext(t, hostname, attrs), metadata.Pairs("job-id", "test-job-id"))
}

func newTestServerWithPolicies(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger())
}

func TestGetPolicies_ReturnsOnlyMatchingPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "web-policy", resp.Policies[0].Name)
}

func TestGetPolicies_EmptyFiltersMatchEveryone(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "all.json", `{"metadata": {"name": "everyone"}}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "anything", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "everyone", resp.Policies[0].Name)
}

func TestGetPolicies_MatchesOnPeerCertLabels(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "node-1", map[string]string{"role": "db"}), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "db-policy", resp.Policies[0].Name)
}

func TestGetPolicies_NoPeerIdentityRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithPolicies(t, dir)

	_, err := srv.GetPolicies(context.Background(), &pb.GetPoliciesRequest{})
	assert.Error(t, err)
}

func TestGetPolicies_MissingJobIDRejected(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	_, err := srv.GetPolicies(peerCertContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	assert.Error(t, err)
}

func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "full.json", `{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.Empty(t, p.ObjectFilters[1].Include)
	assert.Empty(t, p.ObjectFilters[1].Exclude)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
	assert.NotEmpty(t, p.Id)
	assert.NotEmpty(t, p.ObjectFilters[0].Id)
	assert.NotEmpty(t, p.ObjectFilters[1].Id)
	assert.NotEqual(t, p.ObjectFilters[0].Id, p.ObjectFilters[1].Id)
}

func TestListPolicies_ReturnsAllPoliciesRegardlessOfIdentity(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Policies, 2)
}

func TestListPolicies_IncludesClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, []string{"web-*"}, resp.Policies[0].ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.Policies[0].ClientFilters.Labels)
}

func TestGetPolicies_StillOmitsClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Nil(t, resp.Policies[0].ClientFilters)
}

func TestGetPolicies_StoragePolicyStillOmitsClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"client_filters": {"hostnames": ["storage-east-*"]},
		"port": 9400,
		"config": {"backend": "filesystem"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "storage-east-1", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, int32(9400), p.Port)
	assert.JSONEq(t, `{"backend": "filesystem"}`, p.Config)
	assert.Nil(t, p.ClientFilters)
}

func TestGetPolicies_ResponseIncludesType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "backup", resp.Policies[0].Type)
}

func TestListPolicies_ResponseIncludesType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "backup", resp.Policies[0].Type)
}

func TestListPolicies_FilterByTypeReturnsOnlyMatchingType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"port": 9400,
		"config": {"backend": "filesystem"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "storage"})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "east-1-storage", resp.Policies[0].Name)
	assert.Equal(t, "storage", resp.Policies[0].Type)
}

func TestListPolicies_EmptyTypeReturnsEveryType(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"port": 9400,
		"config": {"backend": "filesystem"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Policies, 2)
}

func TestListPolicies_UnknownTypeReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "quux"})
	require.NoError(t, err)
	assert.Empty(t, resp.Policies)
}
