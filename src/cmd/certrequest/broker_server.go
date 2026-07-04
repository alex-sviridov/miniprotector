package main

import (
	"context"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// mintFunc mints a token for hostname/sans. Production wires this to
// certmint.Mint; tests inject a stub so this file's unit tests never touch
// a real CA (that's the e2e test's job, Task 5).
type mintFunc func(hostname string, sans []string) (string, error)

// brokerServer implements EnrollmentBrokerService: the sole RPC an
// enrolled node may call to obtain a fresh CA enrollment token, gated by
// exact hostname match against trustedCaller (client_manager_host from
// local.conf) rather than "any valid cert" -- this RPC is equivalent to
// CA-admin privilege, so it does not use the mesh's normal
// any-cert-is-trusted posture.
type brokerServer struct {
	pb.UnimplementedEnrollmentBrokerServiceServer
	trustedCaller string
	mint          mintFunc
}

func newBrokerServer(trustedCaller string, mint mintFunc) *brokerServer {
	return &brokerServer{trustedCaller: trustedCaller, mint: mint}
}

func (s *brokerServer) MintEnrollmentToken(ctx context.Context, req *pb.MintEnrollmentTokenRequest) (*pb.MintEnrollmentTokenResponse, error) {
	caller, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}
	if caller != s.trustedCaller {
		return nil, fmt.Errorf("caller %q is not the trusted client-manager (%q)", caller, s.trustedCaller)
	}

	hostname := req.GetHostname()
	if hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}

	token, err := s.mint(hostname, req.GetSans())
	if err != nil {
		return nil, fmt.Errorf("mint token for %s: %w", hostname, err)
	}
	return &pb.MintEnrollmentTokenResponse{Token: token}, nil
}
