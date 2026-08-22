package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is policy-server's per-RPC authorization matrix.
// GetPolicies is deliberately absent -- every enrolled node, of every
// role, calls it on a schedule to fetch its own policies and report
// check-ins, including control-plane nodes managing their own agent
// lifecycle. The remaining RPCs are the admin surface api-server proxies
// and are restricted to control-plane callers.
func roleRequirements() map[string][]string {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + svc + "/ListPolicies":      {"control-plane"},
		"/" + svc + "/CreatePolicy":      {"control-plane"},
		"/" + svc + "/UpdatePolicy":      {"control-plane"},
		"/" + svc + "/DeletePolicy":      {"control-plane"},
		"/" + svc + "/GetNodeCertStatus": {"control-plane"},
	}
}
