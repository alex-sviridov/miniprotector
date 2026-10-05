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
	var runs []RunResult
	for i := 1; i <= a.Runs; i++ {
		logf("run %d/%d", i, a.Runs)
		r, err := RunOnce(ctx, a, i, logf)
		if err != nil {
			fmt.Fprintf(stderr, "mpbench: run %d failed: %v\n", i, err)
			return 1
		}
		runs = append(runs, *r)
	}

	sum := Summarize(runs)
	bw := "unlimited"
	if a.Bandwidth > 0 {
		bw = humanBytes(a.Bandwidth) + "/s"
	}
	header := fmt.Sprintf("dataset: %d files, %s (profile %s, dup %.2f, seed %d)   network: rtt %v, bandwidth %s   streams %d, window %d, runs %d",
		runs[0].DatasetFiles, humanBytes(runs[0].DatasetBytes), a.Profile, a.DupRatio, a.Seed, a.RTT, bw, a.Streams, a.Window, a.Runs)
	WriteTable(stdout, header, sum)

	if a.JSONPath != "" {
		rep, err := BuildReport(a, started, runs, sum)
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
