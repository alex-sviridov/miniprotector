package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	svc := pb.CatalogService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + svc + "/SyncFileVersions":      {"store"},
		"/" + svc + "/ListEntries":           {"control-plane"},
		"/" + svc + "/ListClientFacets":      {"control-plane"},
		"/" + svc + "/ListJobFacets":         {"control-plane"},
		"/" + svc + "/ListDirectoryFacets":   {"control-plane"},
		"/" + svc + "/ListStoreFacets":       {"control-plane"},
		"/" + svc + "/ListDirectoryChildren": {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}
