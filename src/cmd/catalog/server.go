package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type catalogServer struct {
	pb.UnimplementedCatalogServiceServer
	store  *catalogstore.Store
	logger *slog.Logger
}

func NewCatalogServer(store *catalogstore.Store, logger *slog.Logger) *catalogServer {
	return &catalogServer{store: store, logger: logger}
}

func (s *catalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	storeNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("SyncFileVersions: could not determine peer identity", "error", err)
		return nil, err
	}

	entries := req.GetEntries()
	batch := make([]catalogstore.Entry, len(entries))
	directoriesByPath := make(map[string]catalogstore.DirectoryAncestor)
	for i, e := range entries {
		var sourceHost, parentDir, shortName string
		if fi, err := filesystem.DecodeFileInfo(e.GetMetadata()); err != nil {
			s.logger.Error("SyncFileVersions: metadata decode failed, entry stored without derived fields",
				"job_id", e.GetJobId(), "object_id", e.GetObjectId(), "error", err)
		} else {
			sourceHost = fi.Source()
			parentDir, shortName = splitPath(fi.Path())
		}
		batch[i] = catalogstore.Entry{
			StoreNode:       storeNode,
			JobID:           e.GetJobId(),
			ObjectID:        e.GetObjectId(),
			Metadata:        e.GetMetadata(),
			Ctime:           e.GetCtime(),
			ExpireAt:        e.GetExpireAt(),
			StoreSeq:        e.GetStoreSeq(),
			StoreCreatedAt:  time.Unix(e.GetCreatedAt(), 0).UTC(),
			SourceHost:      sourceHost,
			ParentDirectory: parentDir,
			ShortFilename:   shortName,
		}
		for _, a := range decodeDirectoryAncestors(parentDir) {
			directoriesByPath[a.Path] = a
		}
	}

	directories := make([]catalogstore.DirectoryAncestor, 0, len(directoriesByPath))
	for _, a := range directoriesByPath {
		directories = append(directories, a)
	}
	if err := s.store.SyncBatch(ctx, batch, directories); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	s.logger.Info("SyncFileVersions: batch persisted", "store_node", storeNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
}

// DeleteFileVersions is the other half of replication: catalogsync tells the
// catalog which versions bwfs has deleted (retention cleanup, a failed job's
// purge) so the web UI never offers a version that no longer exists. As with
// SyncFileVersions the node is the CA-verified mTLS peer, never a request
// field, so one node can only ever delete its own entries. Idempotent.
func (s *catalogServer) DeleteFileVersions(ctx context.Context, req *pb.DeleteVersionsRequest) (*pb.DeleteVersionsResponse, error) {
	storeNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("DeleteFileVersions: could not determine peer identity", "error", err)
		return nil, err
	}
	refs := make([]catalogstore.EntryRef, len(req.GetEntries()))
	for i, e := range req.GetEntries() {
		refs[i] = catalogstore.EntryRef{JobID: e.GetJobId(), ObjectID: e.GetObjectId()}
	}
	deleted, err := s.store.DeleteEntries(ctx, storeNode, refs)
	if err != nil {
		s.logger.Error("DeleteFileVersions: delete failed", "error", err, "count", len(refs))
		return nil, err
	}
	s.logger.Info("DeleteFileVersions: batch applied", "store_node", storeNode, "requested", len(refs), "deleted", deleted)
	return &pb.DeleteVersionsResponse{}, nil
}

// ReportDamagedFiles receives the sending node's complete set of currently
// damaged file ids and makes it that node's set in the catalog. The stream is
// one snapshot, so it is applied only after a clean end of stream: a stream
// that breaks off is a partial set, and applying it would clear damage that is
// still real -- on any other Recv error nothing changes and the error goes
// back to catalogsync, which retries. The ids are accumulated before the
// replace so the single catalog writer is held only for the replace itself,
// never while waiting on the network. As with SyncFileVersions the node is
// the CA-verified mTLS peer, never a request field, so a node can only ever
// replace its own set.
func (s *catalogServer) ReportDamagedFiles(stream pb.CatalogService_ReportDamagedFilesServer) error {
	ctx := stream.Context()
	storeNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("ReportDamagedFiles: could not determine peer identity", "error", err)
		return err
	}

	var objectIDs []string
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.logger.Warn("ReportDamagedFiles: stream ended early, damaged set left unchanged",
				"store_node", storeNode, "received", len(objectIDs), "error", err)
			return err
		}
		objectIDs = append(objectIDs, chunk.GetObjectIds()...)
	}

	if err := s.store.ReplaceDamagedFiles(ctx, storeNode, objectIDs); err != nil {
		s.logger.Error("ReportDamagedFiles: replace failed", "store_node", storeNode, "count", len(objectIDs), "error", err)
		return status.Errorf(codes.Internal, "replace damaged files: %v", err)
	}
	s.logger.Info("ReportDamagedFiles: damaged set replaced", "store_node", storeNode, "count", len(objectIDs))
	return stream.SendAndClose(&pb.ReportDamagedFilesResponse{})
}

// decodeDirectoryAncestors walks parentDir's ancestor chain via splitPath
// -- the same shape-detecting split SyncFileVersions uses to derive
// parentDir itself -- collecting one DirectoryAncestor per level from
// parentDir up to its root, root-first Depth (0 at the root). A blank
// parentDir (a sync-time metadata decode failure) yields no ancestors: an
// unknown location can't be placed in the tree.
func decodeDirectoryAncestors(parentDir string) []catalogstore.DirectoryAncestor {
	if parentDir == "" {
		return nil
	}
	var ancestors []catalogstore.DirectoryAncestor
	current := parentDir
	for current != "" {
		parent, base := splitPath(current)
		name := base
		if parent == "" {
			name = current // true root: display itself, e.g. "/" or "C:\"
		}
		ancestors = append(ancestors, catalogstore.DirectoryAncestor{Path: current, ParentPath: parent, Name: name})
		current = parent
	}
	for i := range ancestors {
		ancestors[i].Depth = len(ancestors) - 1 - i // built leaf-to-root; index from the end for root-first depth
	}
	return ancestors
}

func (s *catalogServer) ListEntries(ctx context.Context, req *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error) {
	records, hasMore, err := s.store.ListEntries(ctx, catalogstore.ListEntriesFilter{
		StoreNode:         req.GetStoreHost(),
		SourceHost:        req.GetSourceHost(),
		Pattern:           req.GetPattern(),
		Limit:             int(req.GetLimit()),
		StartingAfter:     req.GetStartingAfter(),
		ReceivedAfter:     unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore:    unixOrZero(req.GetReceivedBefore()),
		SourceHosts:       req.GetSourceHosts(),
		JobNames:          req.GetJobNames(),
		ParentDirectories: req.GetParentDirectories(),
	})
	if err != nil {
		s.logger.Error("ListEntries: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list entries: %v", err)
	}

	entries := make([]*pb.Entry, len(records))
	for i, rec := range records {
		entries[i] = toProtoEntry(rec)
	}
	return &pb.ListEntriesResponse{Entries: entries, HasMore: hasMore}, nil
}

// toProtoEntry decodes rec.Metadata (a gob-encoded filesystem.FileInfo)
// into Entry's path/size/mode/owner/group/mod_time fields. A decode
// failure (malformed or non-filesystem metadata) leaves those fields at
// their zero values rather than failing the whole ListEntries call --
// one bad row shouldn't hide every other entry in the response. SourceHost
// is NOT decoded here — it's read directly from rec.SourceHost, persisted
// once at sync time in SyncFileVersions. ParentDirectory and ShortFilename
// are the same: persisted columns computed once at sync time, not decoded
// here. Damaged comes from the store's per-row annotation against
// catalog_damaged_files.
func toProtoEntry(rec catalogstore.EntryRecord) *pb.Entry {
	entry := &pb.Entry{
		Id:              rec.ID,
		StoreHost:       rec.StoreNode,
		SourceHost:      rec.SourceHost,
		JobId:           rec.JobID,
		ObjectId:        rec.ObjectID,
		Ctime:           rec.Ctime,
		StoreCreatedAt:  rec.StoreCreatedAt.Unix(),
		ReceivedAt:      rec.ReceivedAt.Unix(),
		ParentDirectory: rec.ParentDirectory,
		ShortFilename:   rec.ShortFilename,
		Damaged:         rec.Damaged,
	}
	if fi, err := filesystem.DecodeFileInfo(rec.Metadata); err == nil {
		entry.Path = fi.Path()
		entry.Size = fi.Size()
		entry.Mode = fi.Mode().String()
		entry.Owner = fi.Owner()
		entry.Group = fi.Group()
		entry.ModTime = fi.Mtime()
	}
	return entry
}

// unixOrZero converts a unix-seconds timestamp to time.Time, leaving the
// zero time.Time{} (rather than the Unix epoch) when ts is 0 -- 0 means
// "no bound" on a ListEntriesRequest/ListFacetsRequest date field, and
// ListEntriesFilter/FacetFilter treat a zero time.Time as unbounded.
func unixOrZero(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func (s *catalogServer) ListClientFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListClientFacets(ctx, catalogstore.FacetFilter{
		ReceivedAfter:     unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore:    unixOrZero(req.GetReceivedBefore()),
		Pattern:           req.GetPattern(),
		JobNames:          req.GetJobNames(),
		ParentDirectories: req.GetParentDirectories(),
	})
	if err != nil {
		s.logger.Error("ListClientFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list client facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}

func (s *catalogServer) ListJobFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListJobFacets(ctx, catalogstore.FacetFilter{
		ReceivedAfter:     unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore:    unixOrZero(req.GetReceivedBefore()),
		Pattern:           req.GetPattern(),
		SourceHosts:       req.GetSourceHosts(),
		ParentDirectories: req.GetParentDirectories(),
	})
	if err != nil {
		s.logger.Error("ListJobFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list job facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}

func (s *catalogServer) ListDirectoryFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListDirectoryFacets(ctx, catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		Pattern:        req.GetPattern(),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListDirectoryFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list directory facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}

func (s *catalogServer) ListStoreFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListStoreFacets(ctx, catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		Pattern:        req.GetPattern(),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListStoreFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list store facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}

func toProtoFacets(facets []catalogstore.Facet) []*pb.Facet {
	out := make([]*pb.Facet, len(facets))
	for i, f := range facets {
		out[i] = &pb.Facet{Name: f.Name, Count: f.Count, LastSeen: f.LastSeen.Unix()}
	}
	return out
}

func (s *catalogServer) ListDirectoryChildren(ctx context.Context, req *pb.ListDirectoryChildrenRequest) (*pb.ListDirectoryChildrenResponse, error) {
	children, err := s.store.ListDirectoryChildren(ctx, req.GetParentPath(), catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListDirectoryChildren: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list directory children: %v", err)
	}
	return &pb.ListDirectoryChildrenResponse{Children: toProtoDirectoryChildren(children)}, nil
}

func toProtoDirectoryChildren(children []catalogstore.DirectoryChild) []*pb.DirectoryChild {
	out := make([]*pb.DirectoryChild, len(children))
	for i, c := range children {
		var lastSeen int64
		if !c.LastSeen.IsZero() {
			lastSeen = c.LastSeen.Unix()
		}
		out[i] = &pb.DirectoryChild{
			Path:        c.Path,
			Name:        c.Name,
			FileCount:   c.FileCount,
			LastSeen:    lastSeen,
			HasChildren: c.HasChildren,
		}
	}
	return out
}
