package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/ListPolicies":      {"control-plane"},
		"/" + svc + "/CreatePolicy":      {"control-plane"},
		"/" + svc + "/UpdatePolicy":      {"control-plane"},
		"/" + svc + "/DeletePolicy":      {"control-plane"},
		"/" + svc + "/GetNodeCertStatus": {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}

func TestRoleRequirements_GetPoliciesIsUnrestricted(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	_, restricted := roleRequirements()["/"+svc+"/GetPolicies"]
	assert.False(t, restricted, "GetPolicies must stay open to every role")
}
