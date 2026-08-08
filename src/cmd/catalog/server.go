package main

import (
	"context"
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
		parentDir, shortName := decodePathParts(e.GetMetadata())
		batch[i] = catalogstore.Entry{
			StoreNode:       storeNode,
			JobID:           e.GetJobId(),
			ObjectID:        e.GetObjectId(),
			Metadata:        e.GetMetadata(),
			Ctime:           e.GetCtime(),
			StoreSeq:        e.GetStoreSeq(),
			StoreCreatedAt:  time.Unix(e.GetCreatedAt(), 0).UTC(),
			SourceHost:      decodeSourceHost(e.GetMetadata()),
			ParentDirectory: parentDir,
			ShortFilename:   shortName,
		}
		for _, a := range decodeDirectoryAncestors(parentDir) {
			directoriesByPath[a.Path] = a
		}
	}

	if err := s.store.EnsureEntries(batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	if len(directoriesByPath) > 0 {
		directories := make([]catalogstore.DirectoryAncestor, 0, len(directoriesByPath))
		for _, a := range directoriesByPath {
			directories = append(directories, a)
		}
		if err := s.store.EnsureDirectories(directories); err != nil {
			s.logger.Error("SyncFileVersions: persisting directory ancestors failed", "error", err, "count", len(directories))
			return nil, err
		}
	}

	s.logger.Info("SyncFileVersions: batch persisted", "store_node", storeNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
}

// decodeDirectoryAncestors walks parentDir's ancestor chain via splitPath
// -- the same shape-detecting split that produced parentDir itself in
// decodePathParts -- collecting one DirectoryAncestor per level from
// parentDir up to its root, root-first Depth (0 at the root). A blank
// parentDir (a decodePathParts failure) yields no ancestors: an unknown
// location can't be placed in the tree.
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

// decodeSourceHost extracts the real originating (backed-up) host from a
// FileVersionEntry's opaque Metadata blob, decoded once at sync time so
// ListEntries can filter on a plain indexed column instead of re-decoding
// Metadata on every read. A decode failure (malformed or non-filesystem
// metadata) yields "" rather than failing the whole batch — one bad entry
// shouldn't block every other entry in it.
func decodeSourceHost(metadata []byte) string {
	fi, err := filesystem.DecodeFileInfo(metadata)
	if err != nil {
		return ""
	}
	return fi.Source()
}

// decodePathParts extracts parent_directory/short_filename from an entry's
// Metadata blob, decoded once at sync time so ListEntries/
// ListDirectoryFacets can filter/group on plain indexed columns instead of
// decoding Metadata per row (see decodeSourceHost above, same rationale). A
// decode failure yields ("", "") rather than failing the whole batch.
func decodePathParts(metadata []byte) (parentDir, shortName string) {
	fi, err := filesystem.DecodeFileInfo(metadata)
	if err != nil {
		return "", ""
	}
	return splitPath(fi.Path())
}

func (s *catalogServer) ListEntries(ctx context.Context, req *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error) {
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
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
// once at sync time (see decodeSourceHost above). ParentDirectory and
// ShortFilename are the same: persisted columns computed once at sync time
// (see decodePathParts), not decoded here.
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
	facets, err := s.store.ListClientFacets(catalogstore.FacetFilter{
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
	facets, err := s.store.ListJobFacets(catalogstore.FacetFilter{
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
	facets, err := s.store.ListDirectoryFacets(catalogstore.FacetFilter{
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

func toProtoFacets(facets []catalogstore.Facet) []*pb.Facet {
	out := make([]*pb.Facet, len(facets))
	for i, f := range facets {
		out[i] = &pb.Facet{Name: f.Name, Count: f.Count, LastSeen: f.LastSeen.Unix()}
	}
	return out
}

func (s *catalogServer) ListDirectoryChildren(ctx context.Context, req *pb.ListDirectoryChildrenRequest) (*pb.ListDirectoryChildrenResponse, error) {
	children, err := s.store.ListDirectoryChildren(req.GetParentPath(), catalogstore.FacetFilter{
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
