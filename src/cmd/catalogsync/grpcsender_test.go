package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type fakeCatalogServer struct {
	pb.UnimplementedCatalogServiceServer
	lastReq    *pb.SyncRequest
	lastDelReq *pb.DeleteVersionsRequest
	err        error
}

func (f *fakeCatalogServer) DeleteFileVersions(ctx context.Context, req *pb.DeleteVersionsRequest) (*pb.DeleteVersionsResponse, error) {
	f.lastDelReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &pb.DeleteVersionsResponse{}, nil
}

func (f *fakeCatalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &pb.SyncResponse{}, nil
}

func newTestGrpcSender(t *testing.T, fake *fakeCatalogServer) *GrpcSender {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterCatalogServiceServer(grpcSrv, fake)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return &GrpcSender{conn: conn, client: pb.NewCatalogServiceClient(conn), timeoutSec: 5}
}

func TestGrpcSender_Send_ConvertsBatchToSingleRequest(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	now := time.Now()
	batch := []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1", Ctime: 100, CreatedAt: now},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2", Ctime: 200, CreatedAt: now},
	}

	require.NoError(t, sender.Send(batch))

	require.NotNil(t, fake.lastReq)
	require.Len(t, fake.lastReq.Entries, 2)
	assert.Equal(t, "obj-1", fake.lastReq.Entries[0].ObjectId)
	assert.Equal(t, "job-1", fake.lastReq.Entries[0].JobId)
	assert.Equal(t, int64(1), fake.lastReq.Entries[0].StoreSeq)
	assert.Equal(t, now.Unix(), fake.lastReq.Entries[0].CreatedAt)
}

func TestGrpcSender_Send_EmptyBatchSendsEmptyRequest(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	require.NoError(t, sender.Send(nil))
	require.NotNil(t, fake.lastReq)
	assert.Empty(t, fake.lastReq.Entries)
}

func TestGrpcSender_Send_RPCErrorPropagates(t *testing.T) {
	fake := &fakeCatalogServer{err: errors.New("boom")}
	sender := newTestGrpcSender(t, fake)

	err := sender.Send([]wfs.FileVersionRecord{{JobID: "job-1", ObjectID: "obj-1"}})
	assert.Error(t, err)
}

var _ Sender = (*GrpcSender)(nil)

func TestGrpcSender_Send_CarriesExpireAt(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)
	exp := int64(1_700_000_000)
	require.NoError(t, sender.Send([]wfs.FileVersionRecord{
		{Seq: 1, JobID: "j", ObjectID: "a", ExpireAt: &exp, CreatedAt: time.Now()},
		{Seq: 2, JobID: "j", ObjectID: "b", CreatedAt: time.Now()},
	}))
	require.Len(t, fake.lastReq.Entries, 2)
	assert.Equal(t, exp, fake.lastReq.Entries[0].ExpireAt)
	assert.Equal(t, int64(0), fake.lastReq.Entries[1].ExpireAt)
}

func TestGrpcSender_SendDeletions_ConvertsBatchToSingleRequest(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	require.NoError(t, sender.SendDeletions([]wfs.FileVersionDeletionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
		{Seq: 2, JobID: "job-2", ObjectID: "obj-2"},
	}))

	require.NotNil(t, fake.lastDelReq)
	require.Len(t, fake.lastDelReq.Entries, 2)
	assert.Equal(t, "job-1", fake.lastDelReq.Entries[0].JobId)
	assert.Equal(t, "obj-2", fake.lastDelReq.Entries[1].ObjectId)
}

func TestGrpcSender_SendDeletions_PropagatesErrors(t *testing.T) {
	fake := &fakeCatalogServer{err: errors.New("catalog down")}
	sender := newTestGrpcSender(t, fake)

	assert.Error(t, sender.SendDeletions([]wfs.FileVersionDeletionRecord{{Seq: 1, JobID: "j", ObjectID: "o"}}))
}
