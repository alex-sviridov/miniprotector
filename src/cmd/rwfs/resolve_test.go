package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestBuildRestoreFilters_OnlyIncludedRulesBecomeFilters(t *testing.T) {
	rules := []RestoreRule{
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 10, NotAfter: 20},
		{Host: "h", Path: "/etc/b", Include: false},
		{Host: "", Path: "/var", Include: true},
	}
	filters, filterToRuleIndex := buildRestoreFilters(rules)
	if len(filters) != 2 {
		t.Fatalf("expected 2 filters (excluded rule skipped), got %d", len(filters))
	}
	if filters[0].GetHost() != "h" || filters[0].GetPath() != "/etc/a" || filters[0].GetPathIsPrefix() {
		t.Fatalf("filter 0 mismatch: %+v", filters[0])
	}
	if filters[0].GetNotBefore() != 10 || filters[0].GetNotAfter() != 20 {
		t.Fatalf("filter 0 timeframe mismatch: %+v", filters[0])
	}
	if !filters[1].GetPathIsPrefix() {
		t.Fatal("host-agnostic rule must become a prefix filter")
	}
	if filterToRuleIndex[0] != 0 || filterToRuleIndex[1] != 2 {
		t.Fatalf("filterToRuleIndex mismatch: %v", filterToRuleIndex)
	}
}

func TestRestoreResolver_KeepsRowMatchingItsOwnRule(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	row := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	dispatch, ruleIndex := resolver.Feed(row, 0)
	if !dispatch {
		t.Fatal("expected the row to be kept")
	}
	if ruleIndex != 0 {
		t.Fatalf("expected the winning rule index to be 0, got %d", ruleIndex)
	}
}

func TestRestoreResolver_DropsRowShadowedByMoreSpecificRule(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true, NotBefore: 1, NotAfter: 100},      // filter 0 -- broad
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 200, NotAfter: 300}, // filter 1 -- specific
	}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/a under BOTH filters (it's under /etc, and it IS
	// /etc/a) -- each with a different version, since their windows differ.
	broadVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	specificVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 20}

	if dispatch, _ := resolver.Feed(broadVersionRow, 0); dispatch {
		t.Fatal("the broad rule's row for /etc/a must be dropped: the specific rule (index 1) governs this path")
	}
	dispatch, ruleIndex := resolver.Feed(specificVersionRow, 1)
	if !dispatch {
		t.Fatal("the specific rule's own row for its own path must be kept")
	}
	if ruleIndex != 1 {
		t.Fatalf("expected the winning rule index to be 1, got %d", ruleIndex)
	}
}

func TestRestoreResolver_DropsRowWhoseWinningRuleIsExcluded(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true},
		{Host: "h", Path: "/etc/secret", Include: false},
	}
	_, filterToRuleIndex := buildRestoreFilters(rules) // only the include rule (index 0) becomes a filter
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/secret under the broad folder filter (filter 0),
	// since the exclude rule never becomes a filter at all.
	row := &pb.FileRow{Source: "h", Path: "/etc/secret", Type: "f", Size: 10}
	if dispatch, _ := resolver.Feed(row, 0); dispatch {
		t.Fatal("the exclude rule governs /etc/secret, so this row must be dropped")
	}
}

func TestRestoreResolver_ZeroByteFileRowIsFoundButNotKept(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	zeroByte := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 0}
	if dispatch, _ := resolver.Feed(zeroByte, 0); dispatch {
		t.Fatal("a zero-byte file row must be found but not selected")
	}
	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a found-but-unselected row must not be reported as not-found, got %v", notFound)
	}
}

func TestRestoreResolver_DirectoryRowIsDispatched(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	dir := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "d"}
	dispatch, ruleIndex := resolver.Feed(dir, 0)
	if !dispatch {
		t.Fatal("a directory row must now be dispatched, not dropped")
	}
	if ruleIndex != 0 {
		t.Fatalf("expected the winning rule index to be 0, got %d", ruleIndex)
	}

	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a dispatched directory must not be reported as not-found, got %v", notFound)
	}
}

// A bounded window that matched nothing gets the distinguished reason:
// the file may well exist on the store, just not inside the window, which
// is a diagnosably different problem from a typo'd path.
func TestRestoreResolver_NotFound_FileLevelFilterWithNoKeptRowIsAFailure(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/missing", Include: true, NotBefore: 100, NotAfter: 200}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)
	// No Feed calls at all -- bwfs never resolved anything for filter 0.

	notFound := resolver.NotFound()
	if len(notFound) != 1 {
		t.Fatalf("expected exactly one not-found entry, got %v", notFound)
	}
	if notFound[0].Host != "h" || notFound[0].Path != "/etc/missing" {
		t.Fatalf("not-found entry mismatch: %+v", notFound[0])
	}
	if notFound[0].Reason != "no version in timeframe" {
		t.Fatalf("expected the distinguished reason, got %q", notFound[0].Reason)
	}
}

// The other half of that discriminator: with no timeframe requested at
// all, the query window covered all of history, so zero rows means the
// file genuinely isn't on this store -- and saying "no version in
// timeframe" there would misdirect the operator toward a window they never
// set. Only one side may be set for the window to count as bounded.
func TestRestoreResolver_NotFound_FileLevelFilterWithNoTimeframeAndNoKeptRowUsesGenericReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rule       RestoreRule
		wantReason string
	}{
		{"unbounded", RestoreRule{Host: "h", Path: "/etc/missing", Include: true}, "not found on this store"},
		{"lower bound only", RestoreRule{Host: "h", Path: "/etc/missing", Include: true, NotBefore: 100}, "no version in timeframe"},
		{"upper bound only", RestoreRule{Host: "h", Path: "/etc/missing", Include: true, NotAfter: 200}, "no version in timeframe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []RestoreRule{tc.rule}
			_, filterToRuleIndex := buildRestoreFilters(rules)
			resolver := newRestoreResolver(rules, filterToRuleIndex)

			notFound := resolver.NotFound()
			if len(notFound) != 1 {
				t.Fatalf("expected exactly one not-found entry, got %v", notFound)
			}
			if notFound[0].Reason != tc.wantReason {
				t.Fatalf("expected reason %q, got %q", tc.wantReason, notFound[0].Reason)
			}
		})
	}
}

func TestRestoreResolver_NotFound_FolderLevelFilterWithNoKeptRowIsNotAFailure(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/empty", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a folder rule matching nothing is a legitimate empty result, got %v", notFound)
	}
}

// TestStreamResolvedRows_DispatchesMatchingRowsAndReportsNotFound drives
// streamResolvedRows over a real bufconn gRPC round trip against
// testResolveServer (defined in verify_test.go, same package) -- one rule
// matches a real row, the other matches nothing and must surface via
// resolver.NotFound() once rows is drained.
func TestStreamResolvedRows_DispatchesMatchingRowsAndReportsNotFound(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.CreateFileData("fs://hosta:f:/etc/a.conf:1000", 4); err != nil {
		t.Fatalf("create file data: %v", err)
	}
	if err := store.FinalizeFileData("fs://hosta:f:/etc/a.conf:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})); err != nil {
		t.Fatalf("finalize file data: %v", err)
	}
	if err := store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/etc/a.conf:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error; err != nil {
		t.Fatalf("create file version record: %v", err)
	}

	listSrv := &testResolveServer{store: store}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	rules := []RestoreRule{
		{Host: "hosta", Path: "/etc/a.conf", Include: true},
		{Host: "hosta", Path: "/etc/missing.conf", Include: true},
	}
	client := pb.NewListServiceClient(conn)
	rowsCh, resolver, errCh := streamResolvedRows(context.Background(), client, rules)

	var got []dispatchedRow
	for r := range rowsCh {
		got = append(got, r)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 dispatched row, got %d: %+v", len(got), got)
	}
	if got[0].Row.GetPath() != "/etc/a.conf" || got[0].RuleIndex != 0 {
		t.Fatalf("unexpected dispatched row: %+v", got[0])
	}

	notFound := resolver.NotFound()
	if len(notFound) != 1 || notFound[0].Path != "/etc/missing.conf" {
		t.Fatalf("expected one not-found entry for /etc/missing.conf, got %+v", notFound)
	}
}

// paddedResolveServer streams rowCount identical, dispatchable rows, each
// padded to ~padBytes via CreatedAt -- a field restoreResolver.Feed never
// looks at, so the padding changes nothing about resolution but makes the
// listing far too large for HTTP/2 flow control to hand the client up front.
// That matters: with a small payload the whole stream is pre-buffered
// client-side, Recv never actually waits, and a backpressure test passes
// vacuously whether the watchdog is fixed or not.
type paddedResolveServer struct {
	pb.UnimplementedListServiceServer
	rowCount int
	padBytes int
}

func (s *paddedResolveServer) ResolveRestoreFiles(_ *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	pad := strings.Repeat("x", s.padBytes)
	for i := 0; i < s.rowCount; i++ {
		if err := stream.Send(&pb.ResolveRestoreFilesResponse{
			Row: &pb.FileRow{
				FileUuid:  fmt.Sprintf("uuid-%d", i),
				Source:    "hosta",
				Type:      "f",
				Path:      "/etc/a.conf",
				Size:      4,
				CreatedAt: pad,
			},
			FilterIndex: 0,
		}); err != nil {
			return err
		}
	}
	return nil
}

// TestStreamResolvedRows_SlowConsumerDoesNotTripTheWatchdog is the
// regression test for the false-positive this final review round found: the
// idle window is meant to measure stream inactivity only, but a single
// blocking `out <- dispatchedRow{...}` hand-off used to be measured too, so
// a consumer that legitimately took longer than streamIdleTimeout to accept
// one row (verifying a large file, or a retry-with-backoff cycle) got its
// perfectly healthy stream cancelled with a bare "context canceled".
//
// The consumer here is slow in both of the ways that matter: steadily (25ms
// per row, so the whole run outlasts the idle window many times over) and,
// once, in a single burst longer than the idle window itself -- the latter is
// what actually reproduces the bug, since a steady drip still touch()es often
// enough to keep the old code alive. Before the pause/resume fix this fails
// with a context-canceled stream error and a short row count; after it, all
// rows arrive and the stream ends cleanly.
func TestStreamResolvedRows_SlowConsumerDoesNotTripTheWatchdog(t *testing.T) {
	const (
		rowCount   = 40
		padBytes   = 256 << 10 // ~10MB total: far past any client-side prebuffer
		perRow     = 25 * time.Millisecond
		burstStall = 400 * time.Millisecond
		burstAtRow = 5
	)
	original := streamIdleTimeout
	streamIdleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = original })

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, &paddedResolveServer{rowCount: rowCount, padBytes: padBytes})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	rules := []RestoreRule{{Host: "hosta", Path: "/etc/a.conf", Include: true}}
	rowsCh, _, errCh := streamResolvedRows(context.Background(), pb.NewListServiceClient(conn), rules)

	got := 0
	for range rowsCh {
		got++
		if got == burstAtRow {
			time.Sleep(burstStall) // one hand-off longer than the whole idle window
			continue
		}
		time.Sleep(perRow)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("a slow but healthy consumer must never cancel the stream, got %v after %d rows", err, got)
	}
	if got != rowCount {
		t.Fatalf("expected all %d rows to be delivered to a slow consumer, got %d", rowCount, got)
	}
}

// stallingResolveServer sends nothing and blocks until its context is
// cancelled -- proves streamResolvedRows's watchdog actually fires on a
// genuinely stalled ResolveRestoreFiles stream, not just a fast one.
type stallingResolveServer struct {
	pb.UnimplementedListServiceServer
}

func (s *stallingResolveServer) ResolveRestoreFiles(_ *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestStreamResolvedRows_WatchdogCancelsAStalledStream(t *testing.T) {
	original := streamIdleTimeout
	streamIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = original })

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, &stallingResolveServer{})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	rules := []RestoreRule{{Host: "hosta", Path: "/etc/a.conf", Include: true}}
	client := pb.NewListServiceClient(conn)
	rowsCh, _, errCh := streamResolvedRows(context.Background(), client, rules)

	for range rowsCh {
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected the stalled stream to surface an error once the watchdog fires")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streamResolvedRows never returned after the watchdog should have fired")
	}
}
