// restore.go implements `rwfs restore` -- this round, a log-only preview
// of a restore policy's resolved file list: for every row
// ResolveRestoreFiles yields that survives restoreResolver.Feed's
// precedence tie-break, it logs the row's source path and its computed
// destination path (restoreDestPath's dest_path rename applied), plus the
// run's overwrite setting once at start. No RestoreFile call, nothing
// written to disk -- see
// docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md.
// Reuses the exact rule-resolution pipeline `rwfs verify --rules-stdin`
// already built (parseRulesStdin, buildRestoreFilters, newRestoreResolver,
// the same not-found semantics) -- only the per-row action differs.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
)

// runRestore resolves --rules-stdin against a remote bwfs store and logs
// what a real restore of this policy would do. jobID rides the
// ResolveRestoreFiles call as outgoing job-id metadata, the same
// convention runVerify uses.
func runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader, quiet bool, certsDir, jobID string) error {
	rules, err := parseRulesStdin(stdin)
	if err != nil {
		return err
	}

	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, quiet, jobID)
}

// runRestoreWithConn is runRestore's body, parameterized on an
// already-dialed conn -- split out purely so tests can exercise it over a
// bufconn dial without duplicating anything past the transport-level
// connect (runRestore itself is the only production caller). See
// restore_test.go's runRestoreWithDialer.
func runRestoreWithConn(logger *slog.Logger, conn *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, jobID string) error {
	callCtx := jobid.Outgoing(context.Background(), jobID)

	filters, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	logger.Info("restore starting", "overwrite", overwrite, "rules", len(rules))

	listClient := pb.NewListServiceClient(conn)
	stream, err := listClient.ResolveRestoreFiles(callCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
	if err != nil {
		return fmt.Errorf("resolve restore files: %w", err)
	}

	total := 0
	warnings := 0
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("resolve restore files: %w", err)
		}

		row := resp.GetRow()
		dispatch, ruleIndex := resolver.Feed(row, resp.GetFilterIndex())
		if !dispatch {
			continue
		}

		total++
		destPath := restoreDestPath(rules[ruleIndex], row.GetPath())
		if !quiet {
			logger.Info("resolved",
				"source", row.GetSource(),
				"path", row.GetPath(),
				"dest_path", destPath,
			)
		}
	}

	for _, nf := range resolver.NotFound() {
		warnings++
		logger.Warn("resolution failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "resolved", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed resolution", warnings)
	}
	return nil
}
