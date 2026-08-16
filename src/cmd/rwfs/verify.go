package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
	"lukechampine.com/blake3"
)

type verifyResult struct {
	fileUUID   string
	source     string
	path       string
	ok         bool
	reason     string
	chunkIndex int64
	size       int64
	chunkCount int32
}

// rulesStdinPayload is the JSON shape read from stdin when --rules-stdin is
// set -- {"rules": [...]}, the same field name policy-server's
// RestorePolicy.Rules and agent's restore.go use.
type rulesStdinPayload struct {
	Rules []RestoreRule `json:"rules"`
}

// notFoundRule records a file-level rule (non-empty Host) that matched no
// row from ResolveRestoreFiles -- reported as a verification failure,
// unlike a folder-level rule (empty Host) matching nothing, which is a
// legitimate outcome (an empty or already-fully-excluded folder), not an
// error. Reason distinguishes a version outside a requested timeframe from
// a path that plain doesn't exist on this store at all -- populated by
// resolve.go's restoreResolver.NotFound.
type notFoundRule struct {
	Host   string
	Path   string
	Reason string
}

// parseRulesStdin reads and validates the --rules-stdin payload.
//
// An empty rule set is rejected rather than accepted as a no-op: it would
// select zero rows and so report success without having verified anything,
// and a one-shot caller (agent's restore task) would record that vacuous
// success as permanently done. agent skips a rules-less policy before it
// ever gets here (cmd/agent/restore.go); this is the belt-and-suspenders
// half of the same guarantee, for any other caller.
func parseRulesStdin(stdin io.Reader) ([]RestoreRule, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read rules from stdin: %w", err)
	}
	var payload rulesStdinPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse rules from stdin: %w", err)
	}
	if len(payload.Rules) == 0 {
		return nil, fmt.Errorf("--rules-stdin requires at least one rule")
	}
	return payload.Rules, nil
}

// runVerify verifies files on a remote bwfs store. jobID rides both the
// ListFiles and the per-file RestoreFile RPCs as outgoing job-id metadata,
// so bwfs's logs for this run correlate with this process's own log -- the
// same convention brfs and policyclient already follow.
func runVerify(logger *slog.Logger, host string, port int, serverName, pathFilter, filter string, rulesStdin bool, stdin io.Reader, streams, retries int, quiet bool, certsDir, jobID string) error {
	// Read and validate the rule set before dialing: it's an argument-shaped
	// error, and ListFiles below is unscoped (see docs/components/rwfs.md),
	// so there's no reason to pay for it only to reject the rules after.
	var rules []RestoreRule
	if rulesStdin {
		parsed, err := parseRulesStdin(stdin)
		if err != nil {
			return err
		}
		rules = parsed
	}

	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runVerifyWithConn(logger, conn, serverName, pathFilter, filter, rulesStdin, rules, streams, retries, quiet, jobID)
}

// runVerifyWithConn is runVerify's body, parameterized on an already-dialed
// conn -- split out purely so tests can exercise it over a bufconn dial
// without duplicating anything past the transport-level connect (runVerify
// itself is the only production caller). See verify_test.go's
// runVerifyWithDialer.
func runVerifyWithConn(logger *slog.Logger, conn *grpc.ClientConn, serverName, pathFilter, filter string, rulesStdin bool, rules []RestoreRule, streams, retries int, quiet bool, jobID string) error {
	// callCtx carries the job-id metadata every RPC below inherits.
	callCtx := jobid.Outgoing(context.Background(), jobID)

	restoreClient := pb.NewRestoreServiceClient(conn)
	workCh := make(chan *pb.FileRow, streams)
	var notFound []notFoundRule
	var resolver *restoreResolver
	streamErrCh := make(chan error, 1)

	if rulesStdin {
		// ResolveRestoreFiles is scoped by the rules themselves (host/path/
		// timeframe), so unlike the plain-verify path below, it never
		// fetches more than the rules could ever select -- see
		// docs/components/rwfs.md.
		filters, filterToRuleIndex := buildRestoreFilters(rules)
		resolver = newRestoreResolver(rules, filterToRuleIndex)

		listClient := pb.NewListServiceClient(conn)
		resolveCtx, resolveCancel := context.WithCancel(callCtx)
		defer resolveCancel()
		stream, err := listClient.ResolveRestoreFiles(resolveCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
		if err != nil {
			return fmt.Errorf("resolve restore files: %w", err)
		}

		go func() {
			defer close(workCh)
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					streamErrCh <- nil
					return
				}
				if err != nil {
					streamErrCh <- fmt.Errorf("resolve restore files: %w", err)
					return
				}
				// resolver.Feed also dispatches directory rows (Type == "d")
				// for restore.go's benefit -- verify has no use for those
				// (a directory's FileUuid is always empty, and RestoreFile
				// answers an empty/unknown file_uuid with NotFound), so
				// gate on row type here too: only a real file ever reaches
				// the worker pool.
				if dispatch, _ := resolver.Feed(resp.GetRow(), resp.GetFilterIndex()); dispatch && resp.GetRow().GetType() == "f" {
					workCh <- resp.GetRow()
				}
			}
		}()
	} else {
		listClient := pb.NewListServiceClient(conn)
		ctx, cancel := context.WithTimeout(callCtx, 30*time.Second)
		resp, err := listClient.ListFiles(ctx, &pb.ListRequest{
			ServerName: serverName,
			Path:       pathFilter,
			Filter:     filter,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}
		go func() {
			defer close(workCh)
			for _, r := range resp.Rows {
				if r.Type == "f" && r.Size > 0 {
					workCh <- r
				}
			}
		}()
		streamErrCh <- nil
	}

	resultCh := make(chan verifyResult, streams)

	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range workCh {
				resultCh <- verifyFileWithRetry(callCtx, logger, restoreClient, row, retries)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	total := 0
	warnings := 0
	for result := range resultCh {
		total++
		if result.ok {
			if !quiet {
				logger.Info("verified",
					"source", result.source,
					"path", result.path,
					"file_uuid", result.fileUUID,
					"chunks", result.chunkCount,
					"size", result.size,
				)
			}
		} else {
			warnings++
			attrs := []any{
				"source", result.source,
				"path", result.path,
				"file_uuid", result.fileUUID,
				"reason", result.reason,
			}
			if result.reason == "blake3_mismatch" {
				attrs = append(attrs, "chunk_index", result.chunkIndex)
			}
			logger.Warn("verification failed", attrs...)
		}
	}

	if streamErr := <-streamErrCh; streamErr != nil {
		return streamErr
	}
	if rulesStdin {
		notFound = resolver.NotFound()
	}

	for _, nf := range notFound {
		warnings++
		logger.Warn("verification failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "verified", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed verification", warnings)
	}
	return nil
}

func verifyFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult {
	var result verifyResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result = verifyFile(ctx, client, row)
		if result.ok || result.reason == "blake3_mismatch" || result.reason == "crc_mismatch" {
			return result
		}
		if attempt < maxRetries {
			logger.Warn("stream error, retrying",
				"path", row.Path,
				"file_uuid", row.FileUuid,
				"attempt", attempt,
				"reason", result.reason,
			)
		}
	}
	return result
}

func verifyFile(parent context.Context, client pb.RestoreServiceClient, row *pb.FileRow) verifyResult {
	base := verifyResult{
		fileUUID: row.FileUuid,
		source:   row.Source,
		path:     row.Path,
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: row.FileUuid})
	if err != nil {
		base.reason = fmt.Sprintf("stream error: %v", err)
		return base
	}

	firstEvent, err := stream.Recv()
	if err != nil {
		base.reason = fmt.Sprintf("stream error: %v", err)
		return base
	}
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.reason = "stream error: expected RestoreFileMeta as first event"
		return base
	}
	base.size = meta.Size
	base.chunkCount = meta.ChunkCount

	hasher := crc32.NewIEEE()

	for {
		event, err := stream.Recv()
		if err != nil {
			base.reason = fmt.Sprintf("stream error: %v", err)
			return base
		}
		chunk := event.GetChunk()
		if chunk == nil {
			base.reason = "stream error: expected RestoreChunk"
			return base
		}

		computed := blake3.Sum256(chunk.Data)
		if !bytes.Equal(computed[:], chunk.Hash) {
			base.reason = "blake3_mismatch"
			base.chunkIndex = chunk.Index
			return base
		}

		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))

		if chunk.Eof {
			break
		}
	}

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	if !bytes.Equal(buf[:], meta.ExpectedChecksum) {
		base.reason = "crc_mismatch"
		return base
	}

	base.ok = true
	return base
}
