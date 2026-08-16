package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestParseRulesStdin_ParsesRules(t *testing.T) {
	rules, err := parseRulesStdin(strings.NewReader(`{"rules":[{"host":"web-01","path":"/a.txt","include":true}]}`))
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "web-01", rules[0].Host)
	assert.Equal(t, "/a.txt", rules[0].Path)
	assert.True(t, rules[0].Include)
}

// An empty rule set must be an error, not a vacuous success: it selects
// nothing, so "verified 0, warnings 0" would look like a real pass and a
// one-shot caller would never run it again.
func TestParseRulesStdin_EmptyRuleSetIsAnError(t *testing.T) {
	for name, payload := range map[string]string{
		"empty array":  `{"rules":[]}`,
		"null rules":   `{"rules":null}`,
		"empty object": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseRulesStdin(strings.NewReader(payload))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "at least one rule")
		})
	}
}

func TestParseRulesStdin_MalformedJSONIsAnError(t *testing.T) {
	_, err := parseRulesStdin(strings.NewReader(`{"rules":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse rules from stdin")
}

// expectedCRC32 computes the file-level CRC32 FinalizeFileData expects,
// the same way verifyFile itself accumulates it (checksum.FeedChunk over
// each chunk's CRC32, big-endian encoded) -- see verify.go's verifyFile.
func expectedCRC32(t *testing.T, chunks [][]byte) []byte {
	t.Helper()
	hasher := crc32.NewIEEE()
	for _, c := range chunks {
		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(c))
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	return buf[:]
}

// testResolveServer is a minimal stand-in for bwfs's real ListServiceServer
// (cmd/bwfs/listserver.go + cmd/bwfs/resolverestorefiles.go). Those types
// live in cmd/bwfs's package main -- Go disallows importing a "package
// main" from anywhere else -- so this package can't reuse the real
// implementation directly for a genuine gRPC/bufconn round-trip test.
// ResolveRestoreFiles here reimplements just enough of
// cmd/bwfs/resolverestorefiles.go's resolveRestoreFilter query (latest
// finalized file_data_records row per file_id, joined against
// file_version_records and restricted to created_at inside the filter's
// [NotBefore, NotAfter] window) to drive a real SQLite query end to end;
// unlike the production query it does not dedupe across multiple content
// versions (file_ids) sharing one (source_host, path) -- no test using
// this fixture needs that, since each seeds at most one file_id per path.
// For a path_is_prefix filter it also emits directory rows straight off
// file_version_records (type = "d"), LIKE-based same as the file query
// above, mirroring the shape bwfs's real query has since Task 3.
type testResolveServer struct {
	pb.UnimplementedListServiceServer
	store *wfs.Store
}

func (s *testResolveServer) ResolveRestoreFiles(req *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	for filterIndex, filter := range req.GetFilters() {
		query := s.store.RawDB().
			Table("file_data_records fd").
			Select("fd.uuid AS uuid, fd.source_host AS source_host, fd.path AS path, fd.size AS size, fd.chunk_count AS chunk_count").
			Joins("JOIN file_version_records fv ON fv.object_id = fd.file_id").
			Where("fd.checksum IS NOT NULL").
			Where("fd.created_at = (SELECT MAX(fd2.created_at) FROM file_data_records fd2 WHERE fd2.file_id = fd.file_id AND fd2.checksum IS NOT NULL)").
			Group("fd.uuid, fd.source_host, fd.path, fd.size, fd.chunk_count").
			Order("fd.source_host ASC, fd.path ASC")

		if filter.GetHost() != "" {
			query = query.Where("fd.source_host = ?", filter.GetHost())
		}
		if filter.GetPathIsPrefix() {
			query = query.Where("fd.path = ? OR fd.path LIKE ?", filter.GetPath(), filter.GetPath()+"/%")
		} else {
			query = query.Where("fd.path = ?", filter.GetPath())
		}
		if filter.GetNotBefore() != 0 {
			query = query.Where("fv.created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
		}
		if filter.GetNotAfter() != 0 {
			query = query.Where("fv.created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
		}

		rows, err := query.Rows()
		if err != nil {
			return err
		}
		sendErr := func() error {
			defer rows.Close()
			for rows.Next() {
				var uuid, source, path string
				var size int64
				var chunkCount int
				if err := rows.Scan(&uuid, &source, &path, &size, &chunkCount); err != nil {
					return err
				}
				if err := stream.Send(&pb.ResolveRestoreFilesResponse{
					Row: &pb.FileRow{
						FileUuid: uuid,
						Source:   source,
						Type:     "f",
						Path:     path,
						Size:     size,
						Chunks:   int32(chunkCount),
					},
					FilterIndex: int32(filterIndex),
				}); err != nil {
					return err
				}
			}
			return rows.Err()
		}()
		if sendErr != nil {
			return sendErr
		}

		if !filter.GetPathIsPrefix() {
			continue
		}
		dirQuery := s.store.RawDB().
			Table("file_version_records").
			Select("source_host, path").
			Where("type = ?", "d").
			Group("source_host, path")
		if filter.GetHost() != "" {
			dirQuery = dirQuery.Where("source_host = ?", filter.GetHost())
		}
		dirQuery = dirQuery.Where("path = ? OR path LIKE ?", filter.GetPath(), filter.GetPath()+"/%")
		if filter.GetNotBefore() != 0 {
			dirQuery = dirQuery.Where("created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
		}
		if filter.GetNotAfter() != 0 {
			dirQuery = dirQuery.Where("created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
		}

		dirRows, err := dirQuery.Rows()
		if err != nil {
			return err
		}
		dirSendErr := func() error {
			defer dirRows.Close()
			for dirRows.Next() {
				var source, path string
				if err := dirRows.Scan(&source, &path); err != nil {
					return err
				}
				if err := stream.Send(&pb.ResolveRestoreFilesResponse{
					Row:         &pb.FileRow{Source: source, Type: "d", Path: path},
					FilterIndex: int32(filterIndex),
				}); err != nil {
					return err
				}
			}
			return dirRows.Err()
		}()
		if dirSendErr != nil {
			return dirSendErr
		}
	}
	return nil
}

// recordingRestoreServer is a minimal RestoreServiceServer that records
// which file_uuids it was asked to restore, then declines to actually serve
// chunks -- it exists solely to prove that runVerifyWithConn's producer
// goroutine actually dispatches a kept row to the worker pool (which calls
// RestoreFile), as distinct from the not-found path, which never does.
// What RestoreFile itself returns is irrelevant to that question, so it
// always fails; verifyFile's own chunk-streaming behavior against a real
// implementation is already covered by TestVerifyFile-shaped coverage
// elsewhere (verifyFile/verifyFileWithRetry are untouched by Task 10).
type recordingRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
	mu        sync.Mutex
	requested []string
}

func (s *recordingRestoreServer) RestoreFile(req *pb.RestoreRequest, _ pb.RestoreService_RestoreFileServer) error {
	s.mu.Lock()
	s.requested = append(s.requested, req.GetFileUuid())
	s.mu.Unlock()
	return status.Error(codes.Unimplemented, "recordingRestoreServer does not serve chunks")
}

func (s *recordingRestoreServer) Requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requested))
	copy(out, s.requested)
	return out
}

// runVerifyWithDialer is a test-only wrapper around runVerify's dial step.
// runVerify always dials via connection.Connect (host/port plus mTLS
// certs), which has no injection seam for a bufconn listener, and no such
// seam exists anywhere else in the codebase either (checked: no bufconn
// use under cmd/rwfs/ or common/connection/ before writing this). This
// duplicates just that dial step against lis, then calls
// runVerifyWithConn -- the exact same package-level resolution/dispatch
// logic runVerify itself calls after dialing -- so everything past the
// transport-level connect in this test is real production code.
func runVerifyWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, rulesJSON string) error {
	t.Helper()

	rules, err := parseRulesStdin(strings.NewReader(rulesJSON))
	require.NoError(t, err)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	return runVerifyWithConn(logger, conn, "", "", "", true, rules, 4, 1, true, "test-job")
}

// TestRunVerify_RulesStdin_UsesResolveRestoreFilesAndReportsTimeframeNotFound
// covers the exclude-by-timeframe path: it not only checks the generic
// failure count (which an UnimplementedRestoreServiceServer would also
// produce if the row were instead incorrectly dispatched and failed for an
// unrelated reason -- see recordingRestoreServer/the sibling dispatch test
// below for the other half of that discrimination) but asserts the actual
// logged reason text, so this test can only pass if resolver.NotFound's
// "no version in timeframe" reason genuinely made it out to the log.
func TestRunVerify_RulesStdin_UsesResolveRestoreFilesAndReportsTimeframeNotFound(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/etc/a.conf:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/etc/a.conf:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/etc/a.conf:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// A rule whose timeframe excludes the only version this file has --
	// exercises the "no version in timeframe" not-found path end to end.
	rulesJSON := `{"rules":[{"host":"hosta","path":"/etc/a.conf","include":true,"not_before":9000,"not_after":9999}]}`

	err = runVerifyWithDialer(t, logger, lis, rulesJSON)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed verification")
	assert.Contains(t, logBuf.String(), `reason="no version in timeframe"`,
		"the not-found log line must carry restoreResolver.NotFound's actual reason text, not just any failure")
	assert.Empty(t, restoreSrv.Requested(),
		"a row excluded by the timeframe must never reach RestoreFile -- if it did, this test's failure count would be a false positive for the wrong reason")
}

// TestRunVerify_RulesStdin_TimeframeIncludingVersionDispatchesToWorkerPool
// is the sibling of the test above: a rule whose timeframe DOES cover the
// file's only version. It proves the row is actually fed into workCh and
// reaches RestoreFile (via recordingRestoreServer), rather than merely
// proving *some* failure occurs -- mutation-testing the "exclude" test
// alone (e.g. zeroing buildRestoreFilters's timeframe, or gating
// resolver.Feed's dispatch behind `false &&`) still produces exactly one
// generic verification failure either way, so that test alone can't tell
// "correctly excluded" apart from "incorrectly never dispatched". This one
// closes that gap on the dispatch side.
func TestRunVerify_RulesStdin_TimeframeIncludingVersionDispatchesToWorkerPool(t *testing.T) {
	const fileID = "fs://hosta:f:/etc/a.conf:1000"

	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData(fileID, 4))
	require.NoError(t, store.FinalizeFileData(fileID, expectedCRC32(t, [][]byte{{1, 2, 3, 4}})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: fileID, JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error)

	var fd wfs.FileDataRecord
	require.NoError(t, store.RawDB().Where("file_id = ?", fileID).First(&fd).Error)
	require.NotEmpty(t, fd.UUID, "seed setup must have produced a finalized file_data_records row")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// Unlike the sibling test above, this rule's [not_before, not_after]
	// window (1000..9999) covers the file's only version (created_at =
	// 5000), so ResolveRestoreFiles must yield the row and resolver.Feed
	// must keep it.
	rulesJSON := `{"rules":[{"host":"hosta","path":"/etc/a.conf","include":true,"not_before":1000,"not_after":9999}]}`

	// The error return is expected and irrelevant here: recordingRestoreServer
	// always fails RestoreFile (it exists to record, not to serve chunks).
	// Only dispatch -- what reached the restore service -- is under test.
	_ = runVerifyWithDialer(t, logger, lis, rulesJSON)

	assert.Equal(t, []string{fd.UUID}, restoreSrv.Requested(),
		"the in-window row must be dispatched to the worker pool and reach RestoreFile with its file_uuid")
}
