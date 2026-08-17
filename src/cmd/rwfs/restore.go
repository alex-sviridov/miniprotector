// restore.go implements `rwfs restore`: for every row streamResolvedRows
// yields (already run through restoreResolver.Feed's precedence
// tie-break), it logs the row's source path and its computed destination
// path (restoreDestPath's dest_path rename applied), plus the run's
// overwrite setting once at start. Once resolution completes with zero
// not-found failures, phase 1 (createRestoreDirectoryStructure) actually
// recreates every resolved directory on the destination filesystem -- file
// content restore (phase 2, still no RestoreFile call) remains unbuilt --
// see docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md.
// Reuses streamResolvedRows, the exact same resolved-row source
// `rwfs verify --rules-stdin` uses (resolve.go) -- only the per-row
// action differs.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

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

	logger.Info("restore starting", "overwrite", overwrite, "rules", len(rules))

	listClient := pb.NewListServiceClient(conn)
	rowsCh, resolver, errCh := streamResolvedRows(callCtx, listClient, rules)

	total := 0
	var dirs []restoreDirectory
	for r := range rowsCh {
		destPath := restoreDestPath(rules[r.RuleIndex], r.Row.GetPath())

		if r.Row.GetType() == "d" {
			dirs = append(dirs, restoreDirectory{DestPath: destPath})
			continue
		}

		total++
		if !quiet {
			logger.Info("resolved",
				"source", r.Row.GetSource(),
				"path", r.Row.GetPath(),
				"dest_path", destPath,
			)
		}
	}
	if err := <-errCh; err != nil {
		return err
	}

	warnings := 0
	for _, nf := range resolver.NotFound() {
		warnings++
		logger.Warn("resolution failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "resolved", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed resolution", warnings)
	}

	return createRestoreDirectoryStructure(logger, dirs)
}

// createRestoreDirectoryStructure is restore's phase 1: recreate every
// resolved directory, parent before child, stopping at the first failure
// (per docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md).
// dirs may contain duplicate DestPaths -- two different rules resolving to
// the same destination -- which this collapses to one create-or-reuse
// rather than flagging as a conflict (see the design's Non-Goals).
func createRestoreDirectoryStructure(logger *slog.Logger, dirs []restoreDirectory) error {
	if len(dirs) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(dirs))
	var unique []restoreDirectory
	for _, d := range dirs {
		if seen[d.DestPath] {
			continue
		}
		seen[d.DestPath] = true
		unique = append(unique, d)
	}
	// Precompute each directory's depth once rather than recomputing
	// ancestorsOrSelfRestorePath inside less on every comparison (O(n log
	// n) redundant allocations for a value that's fixed per element). The
	// depth rides alongside its directory in one struct slice so a sort
	// swap can never desync depth from directory the way two
	// independently-sorted parallel slices could. sort.SliceStable costs
	// nothing extra here -- same-depth directories never nest (a directory
	// can't be its own sibling's parent), so ordering among same-depth
	// entries never affects correctness.
	withDepth := make([]struct {
		dir   restoreDirectory
		depth int
	}, len(unique))
	for i, d := range unique {
		withDepth[i].dir = d
		withDepth[i].depth = len(ancestorsOrSelfRestorePath(d.DestPath))
	}
	sort.SliceStable(withDepth, func(i, j int) bool {
		return withDepth[i].depth < withDepth[j].depth
	})
	for i, wd := range withDepth {
		unique[i] = wd.dir
	}

	logger.Info("creating restored directory structure")
	created, reused := 0, 0
	for _, dir := range unique {
		wasCreated, err := createRestoreDirectory(dir)
		if err != nil {
			logger.Error("failed to create restored directory", "path", dir.DestPath, "reason", err)
			return fmt.Errorf("create restored directory %s: %w", dir.DestPath, err)
		}
		if wasCreated {
			created++
		} else {
			reused++
		}
	}
	logger.Info("restored directory structure created", "created", created, "reused", reused)
	return nil
}
