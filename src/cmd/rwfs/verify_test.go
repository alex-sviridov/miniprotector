package main

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

func TestRunVerify_RulesStdin_UsesResolveRestoreFilesAndReportsTimeframeNotFound(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/etc/a.conf:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/etc/a.conf:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/etc/a.conf:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listSrv := &testResolveServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, &pb.UnimplementedRestoreServiceServer{})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// A rule whose timeframe excludes the only version this file has --
	// exercises the "no version in timeframe" not-found path end to end.
	rulesJSON := `{"rules":[{"host":"hosta","path":"/etc/a.conf","include":true,"not_before":9000,"not_after":9999}]}`

	err = runVerifyWithDialer(t, logger, lis, rulesJSON)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed verification")
}
