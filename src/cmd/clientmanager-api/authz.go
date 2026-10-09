package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is clientmanager-api's per-RPC authorization matrix:
// every RPC is restricted to control-plane callers -- api-server is the
// only intended caller. See
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
func roleRequirements() map[string][]string {
	svc := pb.ClientManagerService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/ListClients": {"control-plane"},
		"/" + svc + "/GetClient":   {"control-plane"},
	}
}
