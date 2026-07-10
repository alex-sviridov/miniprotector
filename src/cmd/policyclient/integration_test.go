//go:build integration

package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

const testCertsDir = "../../common/testdata/certs"

// stubPolicyServer is a minimal, literal pb.PolicyServiceServer -- not
// policy-server's own matching logic (already covered by
// cmd/policy-server's own package tests). It exists only to give this test
// a genuine gRPC+mTLS peer to dial, so fetchAndCache's real network path
// (connection.Connect, a real TLS handshake, real protobuf encoding) is
// exercised at least once, not just its fake-backed unit tests.
type stubPolicyServer struct {
	pb.UnimplementedPolicyServiceServer
	resp *pb.GetPoliciesResponse
}

func (s *stubPolicyServer) GetPolicies(context.Context, *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	return s.resp, nil
}

func TestFetchAndCache_Integration_RealServerRealMTLS(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer lis.Close()

	serverCreds, err := mtls.LoadServerCredentials(testCertsDir)
	require.NoError(t, err)

	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterPolicyServiceServer(grpcSrv, &stubPolicyServer{
		resp: &pb.GetPoliciesResponse{
			Policies: []*pb.Policy{{Name: "real-wire-policy", Rpo: "12h"}},
		},
	})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	_, portStr, err := net.SplitHostPort(lis.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cachePath := t.TempDir() + "/policies-cache.json"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	err = fetchAndCache(testCertsDir, "127.0.0.1", port, 5, cachePath, logger)
	require.NoError(t, err)

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "real-wire-policy")
}
