package mtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
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

func selfSignedCertWithAttributes(t *testing.T, cn string, attrs map[string]string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	value, err := json.Marshal(attrs)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{
			{Id: attributeExtensionOID, Critical: false, Value: value},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func contextWithPeerCert(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

func TestPeerAttributes_ReturnsParsedAttributes(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"role": "prod-db", "env": "prod"})
	ctx := contextWithPeerCert(cert)

	got, err := PeerAttributes(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"role": "prod-db", "env": "prod"}, got)
}

func TestPeerAttributes_NoExtensionReturnsEmptyMap(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)

	got, err := PeerAttributes(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPeerAttributes_MalformedExtensionValueFails(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node-1"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{
			{Id: attributeExtensionOID, Critical: false, Value: []byte("not json")},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	_, err = PeerAttributes(contextWithPeerCert(cert))
	assert.Error(t, err)
}

func TestPeerAttributes_NoPeerInContext(t *testing.T) {
	_, err := PeerAttributes(context.Background())
	assert.Error(t, err)
}

func TestPeerHostnameFromConnState_ReturnsFirstSAN(t *testing.T) {
	cert := loadFixtureCert(t, fixtureCertsDir+"/client.crt")
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}

	host, err := PeerHostnameFromConnState(state)
	require.NoError(t, err)
	assert.Equal(t, "bwfs.internal", host)
}

func TestPeerHostnameFromConnState_FallsBackToCommonName(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "cn-only-node")
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}

	host, err := PeerHostnameFromConnState(state)
	require.NoError(t, err)
	assert.Equal(t, "cn-only-node", host)
}

func TestPeerHostnameFromConnState_NilState(t *testing.T) {
	_, err := PeerHostnameFromConnState(nil)
	assert.Error(t, err)
}

func TestPeerHostnameFromConnState_NoPeerCertificates(t *testing.T) {
	_, err := PeerHostnameFromConnState(&tls.ConnectionState{})
	assert.Error(t, err)
}
