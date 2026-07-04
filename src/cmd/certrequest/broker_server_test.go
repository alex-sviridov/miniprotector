package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name: builds a context carrying a self-signed cert with the given
// hostname as its SAN, simulating a verified mTLS peer identity without a
// real handshake.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
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

func TestMintEnrollmentToken_TrustedCallerMints(t *testing.T) {
	called := false
	mint := func(hostname string, sans []string) (string, error) {
		called = true
		assert.Equal(t, "node-east-01", hostname)
		assert.Equal(t, []string{"node-east-01.internal"}, sans)
		return "tok-abc", nil
	}
	srv := newBrokerServer("client-manager.internal", mint)

	resp, err := srv.MintEnrollmentToken(fakeAuthContext(t, "client-manager.internal"), &pb.MintEnrollmentTokenRequest{
		Hostname: "node-east-01",
		Sans:     []string{"node-east-01.internal"},
	})
	require.NoError(t, err)
	assert.Equal(t, "tok-abc", resp.Token)
	assert.True(t, called)
}

func TestMintEnrollmentToken_UntrustedCallerRejected(t *testing.T) {
	called := false
	mint := func(hostname string, sans []string) (string, error) {
		called = true
		return "tok-abc", nil
	}
	srv := newBrokerServer("client-manager.internal", mint)

	_, err := srv.MintEnrollmentToken(fakeAuthContext(t, "attacker.internal"), &pb.MintEnrollmentTokenRequest{
		Hostname: "node-east-01",
	})
	assert.Error(t, err)
	assert.False(t, called, "mint must not be called for an untrusted caller")
}

func TestMintEnrollmentToken_NoPeerIdentityRejected(t *testing.T) {
	srv := newBrokerServer("client-manager.internal", func(string, []string) (string, error) {
		t.Fatal("mint must not be called without a peer identity")
		return "", nil
	})

	_, err := srv.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{Hostname: "node-east-01"})
	assert.Error(t, err)
}

func TestMintEnrollmentToken_EmptyHostnameRejected(t *testing.T) {
	srv := newBrokerServer("client-manager.internal", func(string, []string) (string, error) {
		t.Fatal("mint must not be called for an empty hostname")
		return "", nil
	})

	_, err := srv.MintEnrollmentToken(fakeAuthContext(t, "client-manager.internal"), &pb.MintEnrollmentTokenRequest{Hostname: ""})
	assert.Error(t, err)
}

func TestMintEnrollmentToken_MintFailurePropagates(t *testing.T) {
	srv := newBrokerServer("client-manager.internal", func(string, []string) (string, error) {
		return "", assert.AnError
	})

	_, err := srv.MintEnrollmentToken(fakeAuthContext(t, "client-manager.internal"), &pb.MintEnrollmentTokenRequest{Hostname: "node-east-01"})
	assert.Error(t, err)
}
