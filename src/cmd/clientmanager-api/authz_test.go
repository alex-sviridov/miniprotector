package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.ClientManagerService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/ListClients": {"control-plane"},
		"/" + svc + "/GetClient":   {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}
