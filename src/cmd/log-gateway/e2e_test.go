//go:build e2e

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
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// TestE2E_PushedLogIsQueryableUnderGatewayEnforcedHostname proves the full
// real pipeline this plan builds: a client presents a real (self-signed
// test CA, operating-tier-shaped) mTLS certificate for "node-real-hostname"
// but declares a spoofed hostname label in its push body; log-gateway,
// running its real TLS server construction (mtls.ServerTLSConfig) and real
// handler, forwards it to a genuine, throwaway Loki container; the pushed
// line is then queryable back out of Loki under the cert-derived hostname,
// never the spoofed one.
func TestE2E_PushedLogIsQueryableUnderGatewayEnforcedHostname(t *testing.T) {
	requireDocker(t)

	lokiURL, cleanup := startTestLoki(t)
	defer cleanup()

	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "log-gateway-e2e", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	certsDir := writeTestCertsDir(t, ca, serverIdentity)

	tlsConfig, err := mtls.ServerTLSConfig(certsDir)
	require.NoError(t, err)

	srv := newLogGatewayServer(lokiURL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
	httpServer := &http.Server{Handler: mux, TLSConfig: tlsConfig}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tlsListener := tls.NewListener(listener, tlsConfig)
	gatewayAddr := listener.Addr().String()

	go func() { _ = httpServer.Serve(tlsListener) }()
	defer httpServer.Close()

	clientCert := generateTestLeaf(t, ca, caKey, "node-real-hostname", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{clientCert},
				RootCAs:      caPool,
				ServerName:   "log-gateway-e2e",
			},
		},
	}

	nowNS := time.Now().UnixNano()
	pushBody := fmt.Sprintf(`{"streams":[{"stream":{"hostname":"spoofed-hostname","binary":"e2e-test"},"values":[["%d","this is the e2e test log line"]]}]}`, nowNS)
	resp, err := httpClient.Post(fmt.Sprintf("https://%s/loki/api/v1/push", gatewayAddr), "application/json", strings.NewReader(pushBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "push failed: %s", body)

	require.Eventually(t, func() bool {
		result, err := queryLoki(lokiURL, `{hostname="node-real-hostname"}`)
		if err != nil {
			return false
		}
		return strings.Contains(result, "this is the e2e test log line")
	}, 15*time.Second, 500*time.Millisecond, "pushed line never became queryable under the gateway-enforced hostname")

	result, err := queryLoki(lokiURL, `{hostname="spoofed-hostname"}`)
	require.NoError(t, err)
	assert.NotContains(t, result, "this is the e2e test log line", "the spoofed hostname label must never have been honored")
}

func queryLoki(lokiURL, query string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, lokiURL+"/loki/api/v1/query_range", nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10))
	q.Set("end", strconv.FormatInt(time.Now().Add(time.Hour).UnixNano(), 10))
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("loki query returned %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

// startTestLoki runs a real, throwaway Loki container directly (not via
// this repo's control-plane compose file, since Loki has no dependency on
// step-ca or any other project component) using the same config
// deploy/control-plane/loki/loki-config.yaml ships. Returns Loki's base
// URL and a cleanup func.
func startTestLoki(t *testing.T) (string, func()) {
	t.Helper()
	repoRoot := repoRootDir(t)
	configPath := filepath.Join(repoRoot, "deploy", "control-plane", "loki", "loki-config.yaml")

	name := fmt.Sprintf("log-gateway-e2e-loki-%d", time.Now().UnixNano())
	runCmd := exec.Command("docker", "run", "-d", "--rm",
		"--name", name,
		"-p", "0:3100",
		"-v", configPath+":/etc/loki/local-config.yaml:ro",
		"grafana/loki:3.7.3",
		"-config.file=/etc/loki/local-config.yaml",
	)
	out, err := runCmd.CombinedOutput()
	require.NoError(t, err, "docker run loki failed: %s", out)

	cleanup := func() {
		_ = exec.Command("docker", "stop", name).Run()
	}

	portCmd := exec.Command("docker", "port", name, "3100")
	portOut, err := portCmd.CombinedOutput()
	if err != nil {
		cleanup()
		require.NoError(t, err, "docker port failed: %s", portOut)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	idx := strings.LastIndex(addr, ":")
	require.GreaterOrEqual(t, idx, 0, "unexpected `docker port` output: %q", addr)
	lokiURL := "http://localhost:" + addr[idx+1:]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, waitForLoki(ctx, lokiURL), "loki never became ready")

	return lokiURL, cleanup
}

func waitForLoki(ctx context.Context, lokiURL string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s/ready: %w (last error: %v)", lokiURL, ctx.Err(), lastErr)
		case <-ticker.C:
			resp, err := http.Get(lokiURL + "/ready")
			if err != nil {
				lastErr = err
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
	}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not found in PATH, skipping e2e test: %v", err)
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon not reachable, skipping e2e test: %v\n%s", err, out)
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// generateTestCA/generateTestLeaf/writeTestCertsDir mirror
// common/mtls/mtls_test.go's helpers of the same name exactly -- Go
// forbids importing another package's _test.go helpers, and this
// codebase's established convention (see cmd/issuer/e2e_test.go's own
// comment on this) is to duplicate small test fixtures per package rather
// than force a shared export.
func generateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

func generateTestLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, hostname string, ekus []x509.ExtKeyUsage, unknownEKUs []asn1.ObjectIdentifier) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:       big.NewInt(2),
		Subject:            pkix.Name{CommonName: hostname},
		DNSNames:           []string{hostname},
		NotBefore:          time.Now().Add(-time.Hour),
		NotAfter:           time.Now().Add(time.Hour),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        ekus,
		UnknownExtKeyUsage: unknownEKUs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{der, ca.Raw},
		PrivateKey:  key,
	}
}

func writeTestCertsDir(t *testing.T, ca *x509.Certificate, serverIdentity tls.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), pemEncodeCert(ca.Raw), 0o600))

	var chainPEM []byte
	for _, der := range serverIdentity.Certificate {
		chainPEM = append(chainPEM, pemEncodeCert(der)...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), chainPEM, 0o600))

	ecKey, ok := serverIdentity.PrivateKey.(*ecdsa.PrivateKey)
	require.True(t, ok)
	keyDER, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), pemEncodeKey(keyDER), 0o600))

	return dir
}

func pemEncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func pemEncodeKey(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
