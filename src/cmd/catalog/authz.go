package main

import pb "github.com/alex-sviridov/miniprotector/api"

// roleRequirements is catalog's per-RPC authorization matrix:
// SyncFileVersions, DeleteFileVersions and ReportDamagedFiles are called only by catalogsync,
// which always runs on a store-role bwfs host; the six List* query RPCs back api-server's
// catalog views and are restricted to control-plane callers.
func roleRequirements() map[string][]string {
	svc := pb.CatalogService_ServiceDesc.ServiceName
	return map[string][]string{
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
}
