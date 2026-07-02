package mtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func loadFixtureCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	certPEM, err := os.ReadFile(path)
	require.NoError(t, err)
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "no PEM block found in %s", path)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

func selfSignedCertNoSAN(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func TestPeerHostname_ReturnsFirstSAN(t *testing.T) {
	cert := loadFixtureCert(t, fixtureCertsDir+"/client.crt")
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})

	host, err := PeerHostname(ctx)
	require.NoError(t, err)
	assert.Equal(t, "bwfs.internal", host)
}

func TestPeerHostname_FallsBackToCommonName(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "cn-only-node")
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})

	host, err := PeerHostname(ctx)
	require.NoError(t, err)
	assert.Equal(t, "cn-only-node", host)
}

func TestPeerHostname_NoPeerInContext(t *testing.T) {
	_, err := PeerHostname(context.Background())
	assert.Error(t, err)
}

func TestPeerHostname_NoTLSAuthInfo(t *testing.T) {
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: nil})
	_, err := PeerHostname(ctx)
	assert.Error(t, err)
}

func TestPeerHostname_NoPeerCertificates(t *testing.T) {
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}},
	})
	_, err := PeerHostname(ctx)
	assert.Error(t, err)
}
