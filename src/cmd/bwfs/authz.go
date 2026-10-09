package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is bwfs's per-RPC authorization matrix, spanning all
// three protocols it serves on one listener (backup, list, restore).
// Every RPC is restricted to client-role callers -- brfs and rwfs are
// the only legitimate callers; catalogsync reads bwfs's local SQLite
// directly rather than over gRPC, so it never appears here.
func roleRequirements() map[string][]string {
	backupSvc := pb.BackupService_ServiceDesc.ServiceName
	listSvc := pb.ListService_ServiceDesc.ServiceName
	restoreSvc := pb.RestoreService_ServiceDesc.ServiceName
	return map[string][]string{
		"/" + backupSvc + "/ProcessBackupStream": {"client"},
		"/" + backupSvc + "/BackupCommit":        {"client"},
		"/" + listSvc + "/ListFiles":             {"client"},
		"/" + listSvc + "/ResolveRestoreFiles":   {"client"},
		"/" + restoreSvc + "/RestoreFile":        {"client"},
	}
}
