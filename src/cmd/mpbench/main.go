// mpbench runs the full backup -> restore -> verify cycle against the real
// brfs, bwfs and rwfs binaries over an emulated network and reports per-phase
// timings and wire bytes. See docs/components/mpbench.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(argv []string, stdout, stderr io.Writer) int {
	a, err := parseArgs(argv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "mpbench:", err)
		return 2
	}
	if err := checkBinaries(a.BinDir); err != nil {
		fmt.Fprintln(stderr, "mpbench:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logf := func(format string, v ...any) { fmt.Fprintf(stderr, format+"\n", v...) }

	started := time.Now()
	variants := Variants(a)
	var results []VariantResult
	for vi, v := range variants {
		var runs []RunResult
		for i := 1; i <= v.Args.Runs; i++ {
			logf("variant %d/%d [%s] run %d/%d", vi+1, len(variants), v.Label, i, v.Args.Runs)
			r, err := RunOnce(ctx, &v.Args, i, logf)
			if err != nil {
				fmt.Fprintf(stderr, "mpbench: variant [%s] run %d failed: %v\n", v.Label, i, err)
				return 1
			}
			runs = append(runs, *r)
		}
		sum := Summarize(runs)
		WriteTable(stdout, tableHeader(&v.Args, v.Label, runs[0]), sum)
		fmt.Fprintln(stdout)
		results = append(results, VariantResult{Label: v.Label, Args: v.Args, Runs: runs, Summary: sum})
	}
	WriteComparison(stdout, results)

	if a.JSONPath != "" {
		rep, err := BuildReport(a, started, results)
		if err == nil {
			err = WriteJSON(a.JSONPath, rep)
		}
		if err != nil {
			fmt.Fprintln(stderr, "mpbench: writing report:", err)
			return 1
		}
		logf("report written to %s", a.JSONPath)
	}
	return 0
}

func tableHeader(a *Args, label string, first RunResult) string {
	bw := "unlimited"
	if a.Bandwidth > 0 {
		bw = humanBytes(a.Bandwidth) + "/s"
	}
	return fmt.Sprintf("[%s] dataset: %d files, %s (profile %s, dup %.2f, seed %d)   network: rtt %v, bandwidth %s   streams %d, window %d, runs %d",
		label, first.DatasetFiles, humanBytes(first.DatasetBytes), a.Profile, a.DupRatio, a.Seed, a.RTT, bw, a.Streams, a.Window, a.Runs)
}
