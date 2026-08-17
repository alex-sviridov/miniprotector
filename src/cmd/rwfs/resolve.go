// resolve.go is the streaming counterpart to applyRulesStdin (verify.go):
// instead of resolving rules against one big, already-fetched slice of
// rows, restoreResolver.Feed is called once per row as bwfs's
// ResolveRestoreFiles response streams in, so memory stays bounded by rule
// count, never by store size. Task 10 rewires runVerify to use this
// instead of applyRulesStdin, and removes applyRulesStdin. See
// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md.
package main

import (
	"context"
	"fmt"
	"io"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// buildRestoreFilters derives one pb.RestoreFileFilter per included rule,
// in rule order -- excluded rules never need file data, they only carve
// exceptions out of a broader include for restoreResolver's precedence
// check, which needs no store data at all. filterToRuleIndex[i] is the
// index into rules that filters[i] came from, letting a caller translate a
// ResolveRestoreFilesResponse's FilterIndex back into a rule identity.
func buildRestoreFilters(rules []RestoreRule) (filters []*pb.RestoreFileFilter, filterToRuleIndex []int) {
	for i, r := range rules {
		if !r.Include {
			continue
		}
		filters = append(filters, &pb.RestoreFileFilter{
			Host:         r.Host,
			Path:         r.Path,
			PathIsPrefix: r.Host == "",
			NotBefore:    r.NotBefore,
			NotAfter:     r.NotAfter,
		})
		filterToRuleIndex = append(filterToRuleIndex, i)
	}
	return filters, filterToRuleIndex
}

// restoreResolver consumes a ResolveRestoreFiles response stream one row
// at a time and decides, per row, whether it should be dispatched for
// verification/restore -- see Feed. It also tracks, per file-level filter,
// whether any row was ever kept, to report NotFound once the stream ends.
type restoreResolver struct {
	rules             []RestoreRule
	filterToRuleIndex []int
	filterFoundAny    []bool
}

func newRestoreResolver(rules []RestoreRule, filterToRuleIndex []int) *restoreResolver {
	return &restoreResolver{
		rules:             rules,
		filterToRuleIndex: filterToRuleIndex,
		filterFoundAny:    make([]bool, len(filterToRuleIndex)),
	}
}

// Feed decides whether row (resolved by filters[filterIndex]) should be
// dispatched. Also returns the winning rule's index, so a caller (e.g.
// rwfs restore, which needs to know which rule's dest_path governs the
// row) can look it up without a second resolution pass -- ruleIndex is -1
// when no rule matched row's path at all (found == false from
// resolveRestoreFileRule below), which today's callers never observe
// (verify.go discards the index outright; restore.go only consults it after
// gating on dispatch), but a future caller must check before indexing r.rules
// with it. It is dropped when: the rule that actually governs row's
// path (per resolveRestoreFileRule's longest-ancestor-wins precedence,
// evaluated fresh per row, over the whole rule list including excludes) is
// excluded or doesn't match filterIndex's own rule -- meaning a more
// specific rule shadows this one for this exact path, and its
// window-resolved version isn't the one that should be used.
//
// Once a row's winning rule is confirmed to be filterIndex's own rule, the
// filter is marked "found" -- existence on the store, tracked independent
// of whether the row is actually dispatched -- before the type/size gate
// below, mirroring applyRulesStdin's existing found-vs-selected split: a
// real zero-byte file or directory row is present on the store (found) but
// has nothing to checksum (not selected), and must not be misreported as
// missing by NotFound. A real, non-empty file or a directory is dispatched;
// anything else (zero-byte file, symlink, or other non-regular/non-directory
// type) is found but not dispatched -- rwfs restore's phase 1 (directory
// creation) and file resolution are the only two things bwfs's data can
// currently be turned into. See
// docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md.
func (r *restoreResolver) Feed(row *pb.FileRow, filterIndex int32) (dispatch bool, ruleIndex int) {
	winningRuleIndex, include, found := resolveRestoreFileRule(r.rules, row.GetSource(), row.GetPath())
	if !found || !include {
		return false, winningRuleIndex
	}
	if winningRuleIndex != r.filterToRuleIndex[filterIndex] {
		return false, winningRuleIndex
	}
	r.filterFoundAny[filterIndex] = true
	isRestorableFile := row.GetType() == "f" && row.GetSize() > 0
	isDirectory := row.GetType() == "d"
	if !isRestorableFile && !isDirectory {
		return false, winningRuleIndex
	}
	return true, winningRuleIndex
}

// NotFound reports, for each file-level (non-host-agnostic) filter that
// never had a matching row observed via Feed (see filterFoundAny above),
// a failure. Call once, after the response stream ends.
//
// The reason discriminates on whether the rule actually requested a
// window, because the two cases point the operator at different problems:
//
//   - Bounded window (NotBefore and/or NotAfter set), zero rows: the file
//     may well exist on this store, just not with any version backed up
//     inside the requested window -- "no version in timeframe". The
//     actionable fix is usually to widen the window.
//   - Unbounded window (neither set), zero rows: the query covered all of
//     history, so the file genuinely isn't on this store at all --
//     "not found on this store", the same generic text applyRulesStdin
//     used before this feature existed. Reporting a timeframe problem
//     here would misdirect toward a window the operator never set.
//
// See the design doc's Error Handling section, which introduces
// "no version in timeframe" explicitly as a case *distinguished from*
// the generic reason, not a replacement for it.
func (r *restoreResolver) NotFound() []notFoundRule {
	var out []notFoundRule
	for i, ruleIndex := range r.filterToRuleIndex {
		if r.filterFoundAny[i] {
			continue
		}
		rule := r.rules[ruleIndex]
		if rule.Host == "" {
			continue // folder-level rule matching nothing is legitimate
		}
		reason := "not found on this store"
		if rule.NotBefore != 0 || rule.NotAfter != 0 {
			reason = "no version in timeframe"
		}
		out = append(out, notFoundRule{Host: rule.Host, Path: rule.Path, Reason: reason})
	}
	return out
}

// dispatchedRow is one row streamResolvedRows has already run through
// restoreResolver.Feed's precedence/type gating and confirmed should be
// acted on. RuleIndex is the winning rule's index into the rules slice
// streamResolvedRows was called with -- restore.go needs it for
// dest_path attribution; verify.go ignores it.
type dispatchedRow struct {
	Row       *pb.FileRow
	RuleIndex int
}

// streamResolvedRows wraps a stall-watchdog-protected ResolveRestoreFiles
// call and resolver.Feed's existing precedence/type gating in one
// reusable stream, used identically by verify --rules-stdin and restore --
// previously each drove this loop independently, in two different shapes.
// The returned resolver's NotFound must only be called once rows is fully
// drained (closed). errCh receives exactly one value (nil on a clean
// end-of-stream, non-nil otherwise), sent as the producer goroutine's
// final act before it closes rows -- so a caller that fully drains rows
// first is guaranteed errCh already has its value ready to read.
func streamResolvedRows(ctx context.Context, client pb.ListServiceClient, rules []RestoreRule) (rows <-chan dispatchedRow, resolver *restoreResolver, errCh <-chan error) {
	filters, filterToRuleIndex := buildRestoreFilters(rules)
	resolver = newRestoreResolver(rules, filterToRuleIndex)

	out := make(chan dispatchedRow)
	errs := make(chan error, 1)

	watchdogCtx, touch, stop := withStallWatchdog(ctx, streamIdleTimeout)

	stream, err := client.ResolveRestoreFiles(watchdogCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
	if err != nil {
		stop()
		close(out)
		errs <- fmt.Errorf("resolve restore files: %w", err)
		return out, resolver, errs
	}

	go func() {
		defer stop()
		defer close(out)
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				errs <- nil
				return
			}
			if err != nil {
				errs <- fmt.Errorf("resolve restore files: %w", err)
				return
			}
			touch()
			if dispatch, ruleIndex := resolver.Feed(resp.GetRow(), resp.GetFilterIndex()); dispatch {
				out <- dispatchedRow{Row: resp.GetRow(), RuleIndex: ruleIndex}
			}
		}
	}()
	return out, resolver, errs
}
