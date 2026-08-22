package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.ClientManagerAdminService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/AddClient":         {"control-plane"},
		"/" + svc + "/ReEnrollClient":    {"control-plane"},
		"/" + svc + "/RevokeClient":      {"control-plane"},
		"/" + svc + "/UnrevokeClient":    {"control-plane"},
		"/" + svc + "/UpdateDescription": {"control-plane"},
		"/" + svc + "/UpdateAttributes":  {"control-plane"},
		"/" + svc + "/UpdateSANs":        {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}
