//go:build e2e

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallstep/certificates/ca"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/connection"
)

const brokerFixtureCertsDir = "../../common/testdata/certs"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func freeTCPPortForServe(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestE2E_ServeMintsRealRedeemableToken proves certrequest serve's
// MintEnrollmentToken RPC, talking to a real throwaway step-ca, returns a
// token certclient's own bootstrap path can actually redeem -- not just
// that the RPC plumbing round-trips (unit tests in broker_server_test.go
// already cover the auth-rejection path with a stubbed minter; this test's
// job is the real certmint.Mint call over a real gRPC/mTLS transport).
func TestE2E_ServeMintsRealRedeemableToken(t *testing.T) {
	requireDocker(t)

	repoRoot := repoRootDir(t)
	tempDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca"), 0o755))
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "deploy", "control-plane", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "entrypoint.sh"), filepath.Join(tempDir, "ca", "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "ca", "entrypoint.sh"), 0o755))

	secretsDir := filepath.Join(tempDir, "ca", "data", "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0o700))
	password := randomPassword(t)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "password"), []byte(password), 0o600))

	projectName := fmt.Sprintf("certrequest-serve-e2e-%d", time.Now().UnixNano())
	compose := func(args ...string) *exec.Cmd {
		cmd := exec.Command("docker", append([]string{"compose", "-p", projectName}, args...)...)
		cmd.Dir = tempDir
		return cmd
	}
	t.Cleanup(func() {
		downCmd := compose("down", "--volumes", "--remove-orphans")
		if out, err := downCmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})
	upCmd := compose("up", "-d", "step-ca")
	out, err := upCmd.CombinedOutput()
	require.NoError(t, err, "docker compose up failed: %s", out)

	hostPort := discoverHostPort(t, compose)
	caURL := fmt.Sprintf("https://localhost:%s", hostPort)
	rootPath := filepath.Join(tempDir, "ca", "data", "certs", "root_ca.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, waitForCA(ctx, caURL, rootPath), "step-ca never became ready")

	mintOpts := certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	}
	mint := func(hostname string, sans []string) (string, error) {
		return certmint.Mint(hostname, sans, mintOpts)
	}

	// The fixture certs dir's identity ("bwfs.internal", per
	// common/mtls's existing test fixtures) is used for both the
	// server's own transport identity and the calling client's identity
	// below -- auth-rejection is already covered by unit tests with a
	// stubbed minter; this test is only about the real minting call.
	srv := newBrokerServer("bwfs.internal", mint)

	grpcPort := freeTCPPortForServe(t)
	serverCtx, stopServer := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(serverCtx, testLogger(), grpcPort, brokerFixtureCertsDir, func(s *grpc.Server) {
			pb.RegisterEnrollmentBrokerServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		stopServer()
		<-errCh
	})
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", grpcPort), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "broker server did not start listening")

	conn, err := connection.Connect("localhost", grpcPort, 5, brokerFixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewEnrollmentBrokerServiceClient(conn)
	resp, err := client.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{Hostname: "e2e-enrolled-host"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Token)

	// Redeem it exactly like certclient's bootstrap path does.
	caClient, err := ca.Bootstrap(resp.Token)
	require.NoError(t, err, "ca.Bootstrap")
	req, _, err := ca.CreateSignRequest(resp.Token)
	require.NoError(t, err, "ca.CreateSignRequest")
	signResp, err := caClient.Sign(req)
	require.NoError(t, err, "Client.Sign")
	leaf, err := ca.Certificate(signResp)
	require.NoError(t, err)
	require.Equal(t, "e2e-enrolled-host", leaf.Subject.CommonName)
}
