package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

func TestRoleRequirements_MatchesAuthorizationMatrix(t *testing.T) {
	backupSvc := pb.BackupService_ServiceDesc.ServiceName
	listSvc := pb.ListService_ServiceDesc.ServiceName
	restoreSvc := pb.RestoreService_ServiceDesc.ServiceName
	want := map[string][]string{
		"/" + backupSvc + "/ProcessBackupStream": {"client"},
		"/" + backupSvc + "/BackupCommit":        {"client"},
		"/" + listSvc + "/ListFiles":             {"client"},
		"/" + listSvc + "/ResolveRestoreFiles":   {"client"},
		"/" + restoreSvc + "/RestoreFile":        {"client"},
	}
	assert.Equal(t, want, roleRequirements())
}

// TestRoleRequirements_CoversEveryGeneratedMethod guards against a new RPC
// being added to any of bwfs's three protos (backup, list, restore)
// without a matching entry in roleRequirements() -- absence from the map
// means "open to every role," which is correct only for the RPCs
// explicitly marked intentionallyOpen.
func TestRoleRequirements_CoversEveryGeneratedMethod(t *testing.T) {
	requirements := roleRequirements()
	intentionallyOpen := map[string]bool{}

	descs := []struct {
		svc  string
		desc grpc.ServiceDesc
	}{
		{pb.BackupService_ServiceDesc.ServiceName, pb.BackupService_ServiceDesc},
		{pb.ListService_ServiceDesc.ServiceName, pb.ListService_ServiceDesc},
		{pb.RestoreService_ServiceDesc.ServiceName, pb.RestoreService_ServiceDesc},
	}

	for _, d := range descs {
		for _, m := range d.desc.Methods {
			full := "/" + d.svc + "/" + m.MethodName
			_, inMap := requirements[full]
			assert.True(t, inMap || intentionallyOpen[full], "%s must be in the role matrix or explicitly marked intentionallyOpen", full)
		}
		for _, s := range d.desc.Streams {
			full := "/" + d.svc + "/" + s.StreamName
			_, inMap := requirements[full]
			assert.True(t, inMap || intentionallyOpen[full], "%s must be in the role matrix or explicitly marked intentionallyOpen", full)
		}
	}
}
