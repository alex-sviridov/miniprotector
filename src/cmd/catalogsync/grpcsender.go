package main

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
)

// GrpcSender delivers a batch to a real catalog service over gRPC — the
// production Sender, used once catalog_host is configured.
type GrpcSender struct {
	conn       *grpc.ClientConn
	client     pb.CatalogServiceClient
	timeoutSec int
}

// NewGrpcSender loads mTLS credentials from certsDir and builds a ClientConn
// for host:port. It does not wait for the catalog to be reachable -- the
// connection is established lazily on the first Send, and gRPC keeps
// retrying/reconnecting for the life of the conn, so a catalog that isn't up
// yet (or restarts independently of catalogsync) never needs catalogsync to
// restart to recover. Errors here mean the mTLS credentials themselves
// couldn't be loaded (missing/corrupt certsDir), not that the catalog is
// unreachable.
func NewGrpcSender(host string, port, timeoutSec int, certsDir string) (*GrpcSender, error) {
	conn, err := connection.DialNonBlocking(host, port, certsDir)
	if err != nil {
		return nil, fmt.Errorf("connect to catalog: %w", err)
	}
	return &GrpcSender{conn: conn, client: pb.NewCatalogServiceClient(conn), timeoutSec: timeoutSec}, nil
}

func (s *GrpcSender) Send(batch []wfs.FileVersionRecord) error {
	entries := make([]*pb.FileVersionEntry, len(batch))
	for i, r := range batch {
		entries[i] = &pb.FileVersionEntry{
			JobId:     r.JobID,
			ObjectId:  r.ObjectID,
			Metadata:  r.Metadata,
			Ctime:     r.Ctime,
			StoreSeq:  r.Seq,
			CreatedAt: r.CreatedAt.Unix(),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.timeoutSec)*time.Second)
	defer cancel()

	if _, err := s.client.SyncFileVersions(ctx, &pb.SyncRequest{Entries: entries}); err != nil {
		return fmt.Errorf("SyncFileVersions: %w", err)
	}
	return nil
}

func (s *GrpcSender) Close() error {
	return s.conn.Close()
}
