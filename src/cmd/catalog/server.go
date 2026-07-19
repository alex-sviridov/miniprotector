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
	for i, e := range entries {
		batch[i] = catalogstore.Entry{
			StoreNode:      storeNode,
			JobID:          e.GetJobId(),
			ObjectID:       e.GetObjectId(),
			Metadata:       e.GetMetadata(),
			Ctime:          e.GetCtime(),
			StoreSeq:       e.GetStoreSeq(),
			StoreCreatedAt: time.Unix(e.GetCreatedAt(), 0).UTC(),
			SourceHost:     decodeSourceHost(e.GetMetadata()),
		}
	}

	if err := s.store.EnsureEntries(batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	s.logger.Info("SyncFileVersions: batch persisted", "store_node", storeNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
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

func (s *catalogServer) ListEntries(ctx context.Context, req *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error) {
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
		StoreNode:     req.GetStoreHost(),
		SourceHost:    req.GetSourceHost(),
		Pattern:       req.GetPattern(),
		Limit:         int(req.GetLimit()),
		StartingAfter: req.GetStartingAfter(),
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
// once at sync time (see decodeSourceHost above).
func toProtoEntry(rec catalogstore.EntryRecord) *pb.Entry {
	entry := &pb.Entry{
		Id:             rec.ID,
		StoreHost:      rec.StoreNode,
		SourceHost:     rec.SourceHost,
		JobId:          rec.JobID,
		ObjectId:       rec.ObjectID,
		Ctime:          rec.Ctime,
		StoreCreatedAt: rec.StoreCreatedAt.Unix(),
		ReceivedAt:     rec.ReceivedAt.Unix(),
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
