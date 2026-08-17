package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
	"lukechampine.com/blake3"
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

// ListFiles is a minimal reimplementation of bwfs's real ListFiles
// handler (cmd/bwfs/listserver.go + cmd/bwfs/list.go's queryFileRows),
// streaming FileRow directly instead of returning one ListResponse --
// enough to drive a real gRPC/bufconn round trip against the streaming
// shape from Task 1. Duplicated, not imported: cmd/bwfs is a different
// "package main" and cannot be imported from here.
func (s *testResolveServer) ListFiles(req *pb.ListRequest, stream pb.ListService_ListFilesServer) error {
	query := s.store.RawDB().
		Table("file_data_records fd").
		Select("fd.uuid AS uuid, fd.source_host AS source_host, fd.path AS path, fd.size AS size, fd.chunk_count AS chunk_count").
		Where("fd.checksum IS NOT NULL").
		Order("fd.source_host ASC, fd.path ASC")
	if req.GetServerName() != "" {
		query = query.Where("fd.source_host = ?", req.GetServerName())
	}
	if req.GetPath() != "" {
		query = query.Where("fd.path LIKE ?", req.GetPath()+"%")
	}

	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uuid, source, path string
		var size int64
		var chunkCount int
		if err := rows.Scan(&uuid, &source, &path, &size, &chunkCount); err != nil {
			return err
		}
		if err := stream.Send(&pb.FileRow{
			FileUuid: uuid,
			Source:   source,
			Type:     "f",
			Path:     path,
			Size:     size,
			Chunks:   int32(chunkCount),
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// failingAfterFirstRowListServer sends exactly one FileRow then returns an
// error -- used by list_test.go (whose consumer never calls RestoreFile,
// so any row content works) and by verify_test.go's plain-path mid-stream
// test (Task 6), which needs Row to carry a real file_uuid a running
// RestoreServiceServer can actually serve.
type failingAfterFirstRowListServer struct {
	pb.UnimplementedListServiceServer
	Row *pb.FileRow
}

func (s *failingAfterFirstRowListServer) ListFiles(_ *pb.ListRequest, stream pb.ListService_ListFilesServer) error {
	if err := stream.Send(s.Row); err != nil {
		return err
	}
	return fmt.Errorf("simulated mid-stream failure")
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

// seedDirectory writes a directory-only file_version_records row -- reused
// from restore_test.go, already defined in this package (package main,
// cmd/rwfs, not cmd/bwfs, so no import needed).

// seedRestorableFile writes a single-chunk file all the way through the
// real storage primitives (StoreChunk, LinkChunkToFileData, CreateFileData,
// FinalizeFileData) plus its file_version_records row, so realRestoreServer
// below can serve it back over a genuine RestoreFile call -- not just a
// row testResolveServer can list. Returns the finalized file's UUID.
func seedRestorableFile(t *testing.T, store *wfs.Store, source, path, jobID string, createdAtUnix int64, data []byte) string {
	t.Helper()
	fileID := fmt.Sprintf("fs://%s:f:%s:%d", source, path, createdAtUnix)

	require.NoError(t, store.CreateFileData(fileID, int64(len(data))))

	hash := blake3.Sum256(data)
	require.NoError(t, store.StoreChunk(hash[:], data))
	require.NoError(t, store.LinkChunkToFileData(hash[:], fileID, 0))

	require.NoError(t, store.FinalizeFileData(fileID, expectedCRC32(t, [][]byte{data})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{
		ObjectID:   fileID,
		JobID:      jobID,
		SourceHost: source,
		Path:       path,
		Type:       "f",
		CreatedAt:  time.Unix(createdAtUnix, 0),
	}).Error)

	var fd wfs.FileDataRecord
	require.NoError(t, store.RawDB().Where("file_id = ?", fileID).First(&fd).Error)
	require.NotEmpty(t, fd.UUID, "seedRestorableFile setup must have produced a finalized file_data_records row")
	return fd.UUID
}

// realRestoreServer is a reimplementation of cmd/bwfs's real
// RestoreServiceServer (cmd/bwfs/restoreserver.go's restoreServer) trimmed
// to what this file's regression test needs: serve a genuinely valid chunk
// stream for a file_uuid seeded via seedRestorableFile above, so a run
// that only ever dispatches real files can actually succeed end to end --
// unlike recordingRestoreServer, which always fails and exists only to
// record what it was asked for. Duplicated, not imported: cmd/bwfs is a
// different "package main" and cannot be imported from here.
type realRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
	store     *wfs.Store
	mu        sync.Mutex
	requested []string
}

// Requested returns every file_uuid RestoreFile was ever asked for, in
// call order -- mirrors recordingRestoreServer.Requested's contract, so a
// test can assert dispatch (what reached RestoreFile) the same way
// regardless of which fixture served the RPC.
func (s *realRestoreServer) Requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requested))
	copy(out, s.requested)
	return out
}

func (s *realRestoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	s.mu.Lock()
	s.requested = append(s.requested, req.GetFileUuid())
	s.mu.Unlock()

	var fd wfs.FileDataRecord
	err := s.store.RawDB().
		Where("uuid = ? AND checksum IS NOT NULL", req.GetFileUuid()).
		First(&fd).Error
	if err != nil {
		return status.Errorf(codes.NotFound, "file_uuid not found or unfinalized: %s", req.GetFileUuid())
	}

	if err := stream.Send(&pb.RestoreEvent{
		Payload: &pb.RestoreEvent_Meta{
			Meta: &pb.RestoreFileMeta{
				Size:             fd.Size,
				ChunkCount:       int32(fd.ChunkCount),
				ExpectedChecksum: fd.Checksum,
			},
		},
	}); err != nil {
		return err
	}

	var links []wfs.FileDataChunkRecord
	if err := s.store.RawDB().
		Where("file_id = ?", fd.FileID).
		Order("`index` ASC").
		Find(&links).Error; err != nil {
		return status.Errorf(codes.Internal, "query chunks: %v", err)
	}

	for i, link := range links {
		hash, err := hex.DecodeString(link.ChunkHash)
		if err != nil {
			return status.Errorf(codes.Internal, "decode chunk hash: %v", err)
		}
		data, err := s.store.ReadChunk(hash)
		if err != nil {
			return status.Errorf(codes.Internal, "read chunk %s: %v", link.ChunkHash, err)
		}
		if err := stream.Send(&pb.RestoreEvent{
			Payload: &pb.RestoreEvent_Chunk{
				Chunk: &pb.RestoreChunk{
					Index: link.Index,
					Hash:  hash,
					Data:  data,
					Eof:   i == len(links)-1,
				},
			},
		}); err != nil {
			return err
		}
	}
	return nil
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

// TestRunVerify_RulesStdin_HostAgnosticFolderRuleSkipsDirectoryRows is the
// regression test for the bug this final review round found: a
// host-agnostic folder rule (empty host) makes buildRestoreFilters set
// PathIsPrefix = true, and ResolveRestoreFiles streams back both the
// file(s) under that path AND the directory row(s) -- restoreResolver.Feed
// dispatches both (Task 5 widened it for restore.go's benefit), but before
// this fix runVerifyWithConn's producer goroutine handed every dispatched
// row straight to the worker pool with no type check, so the directory row
// reached RestoreFile with an empty FileUuid, which bwfs answers NotFound,
// counted as a verification failure -- meaning every folder rule failed
// verification even when its one real file was perfectly fine.
//
// This uses realRestoreServer (not recordingRestoreServer) precisely
// because the fix must be provable end to end: a run that dispatches only
// the file must actually succeed, not merely avoid dispatching the
// directory while still failing for some other reason.
func TestRunVerify_RulesStdin_HostAgnosticFolderRuleSkipsDirectoryRows(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	fileUUID := seedRestorableFile(t, store, "hosta", "/data/photos/a.jpg", "job1", 5000, []byte{1, 2, 3, 4})
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// Host-agnostic folder rule -- buildRestoreFilters sets PathIsPrefix =
	// true for it (rule.Host == ""), so ResolveRestoreFiles streams back
	// both the file and the directory seeded above under /data/photos.
	rulesJSON := `{"rules":[{"host":"","path":"/data/photos","include":true}]}`

	err = runVerifyWithDialer(t, logger, lis, rulesJSON)
	require.NoError(t, err, "a directory row under a host-agnostic folder rule must never fail verification: %s", logBuf.String())
	assert.Equal(t, []string{fileUUID}, restoreSrv.Requested(),
		"only the file's uuid must ever reach RestoreFile -- the directory row must never be dispatched to the worker pool")
}

// runVerifyPlainWithDialer is runVerifyWithDialer's counterpart for the
// plain, non-rules-stdin path: dials a bufconn listener the same way, then
// calls runVerifyWithConn with rulesStdin=false and no rules.
func runVerifyPlainWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, serverName, pathFilter, filter string, quiet bool) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()
	return runVerifyWithConn(logger, conn, serverName, pathFilter, filter, false, nil, 4, 1, quiet, "test-job")
}

// TestRunVerify_PlainPath_StreamsAndVerifiesAllFiles proves the plain
// (non-rules-stdin) path -- previously an atomic unary ListFiles call --
// now streams rows straight into the worker pool and every real file
// still gets verified.
func TestRunVerify_PlainPath_StreamsAndVerifiesAllFiles(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	uuidA := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte{1, 2, 3, 4})
	uuidB := seedRestorableFile(t, store, "hosta", "/data/b.txt", "job1", 1000, []byte{5, 6, 7, 8})

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	err = runVerifyPlainWithDialer(t, logger, lis, "hosta", "", "", true)
	require.NoError(t, err, "plain verify must succeed for two genuinely valid files: %s", logBuf.String())
	assert.ElementsMatch(t, []string{uuidA, uuidB}, restoreSrv.Requested())
}

// TestRunVerify_PlainPath_MidStreamErrorReportedAlongsideSummary proves
// the new mid-stream failure mode (impossible under the old atomic unary
// call): a row received before the failure was legitimately verified and
// counts, and the run still fails overall.
func TestRunVerify_PlainPath_MidStreamErrorReportedAlongsideSummary(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	uuidA := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte{1, 2, 3, 4})

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &failingAfterFirstRowListServer{
		Row: &pb.FileRow{FileUuid: uuidA, Source: "hosta", Path: "/data/a.txt", Type: "f", Size: 4},
	}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	err = runVerifyPlainWithDialer(t, logger, lis, "hosta", "", "", true)
	require.Error(t, err)
	assert.Equal(t, []string{uuidA}, restoreSrv.Requested(),
		"the row received before the mid-stream failure must still have been dispatched and verified")
	assert.Contains(t, logBuf.String(), "summary",
		"the summary line must still be logged even when the stream fails mid-run, not suppressed by the stream error")
}

// TestVerifyFileWithRetry_BacksOffBetweenAttempts proves real backoff
// happens between attempts (not mocked time) -- recordingRestoreServer
// always fails RestoreFile with a plain stream error (never a checksum
// mismatch), so every attempt short of the last one waits.
func TestVerifyFileWithRetry_BacksOffBetweenAttempts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewRestoreServiceClient(conn)
	row := &pb.FileRow{FileUuid: "does-not-exist", Source: "hosta", Path: "/x"}

	start := time.Now()
	verifyFileWithRetry(context.Background(), logger, client, row, 3)
	elapsed := time.Since(start)

	// 3 attempts -> 2 waits; backoff starts at 500ms and doubles, so the
	// floor is 500ms + 1s = 1.5s (well under the 5s cap). Assert a
	// slightly relaxed floor to absorb scheduler jitter.
	if elapsed < 1300*time.Millisecond {
		t.Fatalf("expected at least ~1.5s of backoff across 2 waits, took %v", elapsed)
	}
}

// stallingRestoreServer sends nothing and blocks until its context is
// cancelled -- proves verifyFile's watchdog actually fires on a
// genuinely stalled RestoreFile stream.
type stallingRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
}

func (s *stallingRestoreServer) RestoreFile(_ *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestVerifyFile_WatchdogCancelsAStalledRestoreFileStream(t *testing.T) {
	original := streamIdleTimeout
	streamIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = original })

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, &stallingRestoreServer{})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewRestoreServiceClient(conn)
	row := &pb.FileRow{FileUuid: "x", Source: "hosta", Path: "/x"}

	done := make(chan verifyResult, 1)
	go func() { done <- verifyFile(context.Background(), client, row) }()

	select {
	case result := <-done:
		require.False(t, result.ok)
		assert.Contains(t, result.reason, "stream error")
	case <-time.After(2 * time.Second):
		t.Fatal("verifyFile never returned after the watchdog should have fired")
	}
}
