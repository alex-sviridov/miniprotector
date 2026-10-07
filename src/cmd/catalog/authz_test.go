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
		"/" + svc + "/DeleteFileVersions":    {"store"},
		"/" + svc + "/ReportDamagedFiles":    {"store"},
		"/" + svc + "/ListEntries":           {"control-plane"},
		"/" + svc + "/ListClientFacets":      {"control-plane"},
		"/" + svc + "/ListJobFacets":         {"control-plane"},
		"/" + svc + "/ListDirectoryFacets":   {"control-plane"},
		"/" + svc + "/ListStoreFacets":       {"control-plane"},
		"/" + svc + "/ListDirectoryChildren": {"control-plane"},
	}
	assert.Equal(t, want, roleRequirements())
}

// TestRoleRequirements_CoversEveryGeneratedMethod guards against a new RPC
// being added to catalog.proto without a matching entry in
// roleRequirements() -- absence from the map means "open to every role,"
// which is correct only for the RPCs explicitly marked intentionallyOpen.
func TestRoleRequirements_CoversEveryGeneratedMethod(t *testing.T) {
	svc := pb.CatalogService_ServiceDesc.ServiceName
	requirements := roleRequirements()
	intentionallyOpen := map[string]bool{}

	for _, m := range pb.CatalogService_ServiceDesc.Methods {
		full := "/" + svc + "/" + m.MethodName
		_, inMap := requirements[full]
		assert.True(t, inMap || intentionallyOpen[full], "%s must be in the role matrix or explicitly marked intentionallyOpen", full)
	}
	for _, s := range pb.CatalogService_ServiceDesc.Streams {
		full := "/" + svc + "/" + s.StreamName
		_, inMap := requirements[full]
		assert.True(t, inMap || intentionallyOpen[full], "%s must be in the role matrix or explicitly marked intentionallyOpen", full)
	}
}
