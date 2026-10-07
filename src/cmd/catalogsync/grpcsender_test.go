package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
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

	// ReportDamagedFiles: the chunks of the last stream and how it ended,
	// guarded by mu since the handler runs on the server's goroutine.
	mu             sync.Mutex
	damagedChunks  [][]string
	damagedEOF     bool // the client closed the stream cleanly
	damagedRecvErr error
}

func (f *fakeCatalogServer) ReportDamagedFiles(stream grpc.ClientStreamingServer[pb.DamagedFilesChunk, pb.ReportDamagedFilesResponse]) error {
	var chunks [][]string
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.mu.Lock()
			f.damagedChunks, f.damagedRecvErr = chunks, err
			f.mu.Unlock()
			return err
		}
		chunks = append(chunks, chunk.ObjectIds)
	}
	f.mu.Lock()
	f.damagedChunks, f.damagedEOF = chunks, true
	f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	return stream.SendAndClose(&pb.ReportDamagedFilesResponse{})
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

func TestGrpcSender_SendDamaged_StreamsEveryPageAsAChunk(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	require.NoError(t, sender.SendDamaged(pagesOf([]string{"a", "b"}, []string{"c"})))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.True(t, fake.damagedEOF)
	assert.Equal(t, [][]string{{"a", "b"}, {"c"}}, fake.damagedChunks)
}

// An empty set is an empty stream, which the catalog takes as "nothing is
// damaged any more".
func TestGrpcSender_SendDamaged_EmptySetIsACleanEmptyStream(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)

	require.NoError(t, sender.SendDamaged(pagesOf()))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.True(t, fake.damagedEOF)
	assert.Empty(t, fake.damagedChunks)
}

func TestGrpcSender_SendDamaged_ServerErrorPropagates(t *testing.T) {
	fake := &fakeCatalogServer{err: errors.New("catalog down")}
	sender := newTestGrpcSender(t, fake)

	assert.Error(t, sender.SendDamaged(pagesOf([]string{"a"})))
}

// A page that fails to read must abort the stream rather than end it
// cleanly, or the catalog would replace its set with a truncated one.
func TestGrpcSender_SendDamaged_PageErrorAbortsTheStream(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)
	calls := 0
	nextPage := func() ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"a"}, nil
		}
		return nil, errors.New("database is locked")
	}

	err := sender.SendDamaged(nextPage)

	assert.ErrorContains(t, err, "database is locked")
	require.Eventually(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.damagedRecvErr != nil
	}, time.Second, 5*time.Millisecond, "the server must see the stream fail")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.False(t, fake.damagedEOF)
}
