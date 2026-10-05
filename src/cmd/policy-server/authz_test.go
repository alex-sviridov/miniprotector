package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/ListPolicies":             {"control-plane"},
		"/" + svc + "/CreatePolicy":             {"control-plane"},
		"/" + svc + "/UpdatePolicy":             {"control-plane"},
		"/" + svc + "/DeletePolicy":             {"control-plane"},
		"/" + svc + "/GetNodeCertStatus":        {"control-plane"},
		"/" + svc + "/ReorderRetentionPolicies": {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}

func TestRoleRequirements_GetPoliciesIsUnrestricted(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	_, restricted := roleRequirements()["/"+svc+"/GetPolicies"]
	assert.False(t, restricted, "GetPolicies must stay open to every role")
}

// TestRoleRequirements_CoversEveryGeneratedMethod guards against a new RPC
// being added to policyserver.proto without a matching entry in
// roleRequirements() -- absence from the map means "open to every role,"
// which is correct only for GetPolicies (explicitly marked
// intentionallyOpen below) and would be a silent security regression for
// anything else.
func TestRoleRequirements_CoversEveryGeneratedMethod(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	requirements := roleRequirements()
	intentionallyOpen := map[string]bool{
		"/" + svc + "/GetPolicies": true,
	}

	for _, m := range pb.PolicyService_ServiceDesc.Methods {
		full := "/" + svc + "/" + m.MethodName
		_, inMap := requirements[full]
		assert.True(t, inMap || intentionallyOpen[full], "%s must be in the role matrix or explicitly marked intentionallyOpen", full)
	}
	for _, s := range pb.PolicyService_ServiceDesc.Streams {
		full := "/" + svc + "/" + s.StreamName
		_, inMap := requirements[full]
		assert.True(t, inMap || intentionallyOpen[full], "%s must be in the role matrix or explicitly marked intentionallyOpen", full)
	}
}
