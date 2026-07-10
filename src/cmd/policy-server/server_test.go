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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// attributeExtensionOID mirrors cmd/issuer/e2e_test.go's own copy -- the
// same private-use OID issuer embeds attributes under; small OID constants
// like this are duplicated per test file in this codebase rather than
// exported from common/mtls.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// fakeAuthContext mirrors cmd/catalog/server_test.go's and cmd/issuer/
// server_test.go's helper of the same name: a self-signed cert with the
// given hostname as its SAN and attributes as its embedded extension,
// simulating a verified mTLS peer identity without a real handshake.
func fakeAuthContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
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

func newTestServerWithPolicies(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, testLogger())
}

func TestGetPolicies_ReturnsOnlyMatchingPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, dir, "db.json", `{
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
	writePolicyFile(t, dir, "all.json", `{"metadata": {"name": "everyone"}}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "anything", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "everyone", resp.Policies[0].Name)
}

func TestGetPolicies_MatchesOnPeerCertLabels(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "db.json", `{
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

func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "full.json", `{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www"}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"]
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
}
