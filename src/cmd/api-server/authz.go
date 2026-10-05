package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is api-server's per-RPC authorization matrix for its
// gRPC surface (its REST API is unauthenticated by role -- see
// docs/SECURITY.md). GetPolicyJobStatus is called only by policy-server's
// restore-cleanup sweep, so it is restricted to the "control-plane" role,
// mirroring cmd/policy-server/authz.go's identical pattern.
func roleRequirements() map[string][]string {
	svc := pb.JobStatusService_ServiceDesc.ServiceName
	storageSvc := pb.StorageStatusService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/GetPolicyJobStatus": {"control-plane"},
		// Posted by each bwfs server itself, enrolled with the "store" role.
		"/" + storageSvc + "/ReportStorageStatus": {"store"},
	}
}
