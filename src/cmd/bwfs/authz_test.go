package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
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
