package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is clientmanager-admin-api's per-RPC authorization
// matrix: every RPC is CA-admin-equivalent and restricted to
// control-plane callers -- api-server is the only intended caller. This
// is the centerpiece fix of
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md: an
// operating certificate that isn't control-plane-role (e.g. a
// compromised bwfs/brfs/rwfs node's) is rejected here before any handler
// runs.
func roleRequirements() map[string][]string {
	svc := pb.ClientManagerAdminService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/AddClient":         {"control-plane"},
		"/" + svc + "/ReEnrollClient":    {"control-plane"},
		"/" + svc + "/RevokeClient":      {"control-plane"},
		"/" + svc + "/UnrevokeClient":    {"control-plane"},
		"/" + svc + "/UpdateDescription": {"control-plane"},
		"/" + svc + "/UpdateAttributes":  {"control-plane"},
		"/" + svc + "/UpdateSANs":        {"control-plane"},
	}
}
