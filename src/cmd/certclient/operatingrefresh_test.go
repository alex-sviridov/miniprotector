package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func operatingRefreshTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeTestBootstrapCred writes a self-signed bootstrap.crt/bootstrap.key
// pair with the given CommonName into certsDir -- runOperatingRefresh only
// ever reads the CommonName back out of it, so a self-signed fixture is
// sufficient; it never needs to chain to a real CA for this test.
func writeTestBootstrapCred(t *testing.T, certsDir, hostname string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	require.NoError(t, os.WriteFile(filepath.Join(certsDir, "bootstrap.crt"), certPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(certsDir, "bootstrap.key"), keyPEM, 0o600))
}

type fakeIssuerClient struct {
	sans     []string
	sansErr  error
	certResp *pb.RequestOperatingCertResponse
	certErr  error
	gotCSR   *x509.CertificateRequest
}

func (f *fakeIssuerClient) DescribeSANs(_ context.Context, _ *pb.DescribeSANsRequest, _ ...grpc.CallOption) (*pb.DescribeSANsResponse, error) {
	if f.sansErr != nil {
		return nil, f.sansErr
	}
	return &pb.DescribeSANsResponse{Sans: f.sans}, nil
}

func (f *fakeIssuerClient) RequestOperatingCert(_ context.Context, req *pb.RequestOperatingCertRequest, _ ...grpc.CallOption) (*pb.RequestOperatingCertResponse, error) {
	if f.certErr != nil {
		return nil, f.certErr
	}
	csr, err := x509.ParseCertificateRequest(req.GetCsrDer())
	if err != nil {
		return nil, err
	}
	f.gotCSR = csr
	return f.certResp, nil
}

func TestRunOperatingRefresh_Success_WritesClientCrtWithMatchingCSR(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")

	fake := &fakeIssuerClient{
		sans:     []string{"node-1.internal"},
		certResp: &pb.RequestOperatingCertResponse{CertChainPem: []byte("fake-chain")},
	}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-chain"), got)

	require.NotNil(t, fake.gotCSR)
	assert.Equal(t, "node-1", fake.gotCSR.Subject.CommonName)
	// DNSNames must be hostname+sans, matching what certmint.Mint actually
	// authorizes (append([]string{hostname}, sans...)) -- not sans alone.
	assert.Equal(t, []string{"node-1", "node-1.internal"}, fake.gotCSR.DNSNames)

	_, err = os.Stat(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err, "client.key should have been generated")
}

func TestRunOperatingRefresh_ReusesExistingOperatingKey(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")
	fake := &fakeIssuerClient{certResp: &pb.RequestOperatingCertResponse{CertChainPem: []byte("chain-1")}}

	require.NoError(t, runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger()))
	keyAfterFirst, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	fake2 := &fakeIssuerClient{certResp: &pb.RequestOperatingCertResponse{CertChainPem: []byte("chain-2")}}
	require.NoError(t, runOperatingRefresh(context.Background(), certsDir, fake2, operatingRefreshTestLogger()))
	keyAfterSecond, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	assert.Equal(t, keyAfterFirst, keyAfterSecond, "client.key must be byte-for-byte unchanged across refreshes")
}

func TestRunOperatingRefresh_DescribeSANsErrorPropagates_NoClientCrtWritten(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")
	fake := &fakeIssuerClient{sansErr: assert.AnError}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunOperatingRefresh_RequestOperatingCertErrorPropagates_NoClientCrtWritten(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")
	fake := &fakeIssuerClient{certErr: assert.AnError}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunOperatingRefresh_MissingBootstrapCredErrors(t *testing.T) {
	certsDir := t.TempDir() // no bootstrap.crt/bootstrap.key written
	fake := &fakeIssuerClient{}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	assert.Error(t, err)
}
