package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Args are mpbench's parsed, validated command-line settings.
type Args struct {
	BinDir    string
	Files     int
	Profile   string
	DupRatio  float64
	Shift     bool
	Seed      uint64
	RTT       time.Duration // full round trip; the proxy delays each direction by RTT/2
	Bandwidth int64         // bytes per second per direction; 0 = unlimited
	Streams   int
	Window    int // 0 = leave brfs's own default
	BrfsArgs  []string
	RwfsArgs  []string
	Runs      int
	JSONPath  string
	Keep      bool
	ColdCache bool // drop the page cache before each phase (needs root)

	// Sweeps run the full cycle once per value (or per combination); a swept
	// dimension overrides its scalar flag.
	SweepWindows []int
	SweepStreams []int
	SweepRTTs    []time.Duration

	// ConfLines are extra key=value lines appended to the generated local.conf,
	// so any config key (default_window, grpc_window_bytes, ...) can be set.
	ConfLines []string
}

var validProfiles = map[string]bool{"small": true, "mixed": true, "large": true, "sparse": true}

func parseArgs(argv []string, stderr io.Writer) (*Args, error) {
	a := &Args{}
	fs := flag.NewFlagSet("mpbench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var bandwidth, brfsArgs, rwfsArgs string
	fs.StringVar(&a.BinDir, "bin-dir", "", "directory holding the brfs, bwfs and rwfs binaries (required)")
	fs.IntVar(&a.Files, "files", 500, "number of files in the generated dataset")
	fs.StringVar(&a.Profile, "profile", "mixed", "file size profile: small, mixed, large or sparse")
	fs.Float64Var(&a.DupRatio, "dup-ratio", 0.3, "fraction of full 64KB blocks drawn from a shared pool (0..1)")
	fs.BoolVar(&a.Shift, "shift", false, "prefix each file with 1-4095 random bytes so duplicate blocks sit off 64KB alignment (what content-defined chunking is for)")
	fs.Uint64Var(&a.Seed, "seed", 1, "dataset seed; the same seed gives byte-identical data")
	fs.DurationVar(&a.RTT, "rtt", 0, "emulated round-trip time, e.g. 50ms")
	fs.StringVar(&bandwidth, "bandwidth", "", "emulated bandwidth per direction, e.g. 100mbit, 1gbit, 10mbyte (default unlimited)")
	fs.IntVar(&a.Streams, "streams", 4, "--streams passed to brfs and rwfs")
	fs.IntVar(&a.Window, "window", 0, "--window passed to brfs (0 = brfs's own default)")
	fs.StringVar(&brfsArgs, "brfs-args", "", "extra arguments for brfs, space separated")
	fs.StringVar(&rwfsArgs, "rwfs-args", "", "extra arguments for rwfs restore, space separated")
	fs.IntVar(&a.Runs, "runs", 3, "number of full cycles to run")
	fs.StringVar(&a.JSONPath, "json", "", "also write the full report as JSON to this path")
	fs.BoolVar(&a.Keep, "keep", false, "keep the work directory of each run")
	fs.BoolVar(&a.ColdCache, "cold-cache", false, "drop the Linux page cache before each phase so reads hit the disk (needs root; warns and continues otherwise)")
	var sweepWindow, sweepStreams, sweepRTT, confLines string
	fs.StringVar(&confLines, "conf", "", "comma-separated key=value lines appended to the generated local.conf, e.g. grpc_window_bytes=4194304")
	fs.StringVar(&sweepWindow, "sweep-window", "", "comma-separated --window values to compare, e.g. 1,4,8 (0 = brfs default)")
	fs.StringVar(&sweepStreams, "sweep-streams", "", "comma-separated --streams values to compare, e.g. 2,4,8")
	fs.StringVar(&sweepRTT, "sweep-rtt", "", "comma-separated --rtt values to compare, e.g. 0,20ms,100ms")
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var err error
	if a.Bandwidth, err = parseBandwidth(bandwidth); err != nil {
		return nil, err
	}
	if a.SweepWindows, err = parseIntList("--sweep-window", sweepWindow, 0); err != nil {
		return nil, err
	}
	if a.SweepStreams, err = parseIntList("--sweep-streams", sweepStreams, 1); err != nil {
		return nil, err
	}
	if a.SweepRTTs, err = parseDurationList("--sweep-rtt", sweepRTT); err != nil {
		return nil, err
	}
	if a.ConfLines, err = parseConfLines(confLines); err != nil {
		return nil, err
	}
	a.BrfsArgs = strings.Fields(brfsArgs)
	a.RwfsArgs = strings.Fields(rwfsArgs)

	switch {
	case a.BinDir == "":
		return nil, fmt.Errorf("--bin-dir is required")
	case a.Files <= 0:
		return nil, fmt.Errorf("--files must be positive, got %d", a.Files)
	case !validProfiles[a.Profile]:
		return nil, fmt.Errorf("--profile must be small, mixed, large or sparse, got %q", a.Profile)
	case a.DupRatio < 0 || a.DupRatio > 1:
		return nil, fmt.Errorf("--dup-ratio must be between 0 and 1, got %v", a.DupRatio)
	case a.RTT < 0:
		return nil, fmt.Errorf("--rtt must not be negative, got %v", a.RTT)
	case a.Streams <= 0:
		return nil, fmt.Errorf("--streams must be positive, got %d", a.Streams)
	case a.Window < 0:
		return nil, fmt.Errorf("--window must not be negative, got %d", a.Window)
	case a.Runs <= 0:
		return nil, fmt.Errorf("--runs must be positive, got %d", a.Runs)
	}
	return a, nil
}

// parseBandwidth turns "100mbit", "10mbyte" and the like into bytes per
// second (decimal units). Empty or "0" means unlimited.
func parseBandwidth(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"kbit", 1e3 / 8}, {"mbit", 1e6 / 8}, {"gbit", 1e9 / 8},
		{"kbyte", 1e3}, {"mbyte", 1e6}, {"gbyte", 1e9},
	}
	for _, u := range units {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
		if err != nil || n <= 0 {
			break
		}
		return int64(n * u.mult), nil
	}
	return 0, fmt.Errorf("invalid bandwidth %q (use e.g. 100mbit, 1gbit, 10mbyte)", s)
}

func splitList(flagName, s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
		if parts[i] == "" {
			return nil, fmt.Errorf("%s: empty element in %q", flagName, s)
		}
	}
	return parts, nil
}

func parseIntList(flagName, s string, min int) ([]int, error) {
	parts, err := splitList(flagName, s)
	if err != nil || parts == nil {
		return nil, err
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < min {
			return nil, fmt.Errorf("%s: %q must be an integer >= %d", flagName, p, min)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseDurationList(flagName, s string) ([]time.Duration, error) {
	parts, err := splitList(flagName, s)
	if err != nil || parts == nil {
		return nil, err
	}
	out := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		d, err := time.ParseDuration(p)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("%s: %q must be a non-negative duration like 50ms", flagName, p)
		}
		out = append(out, d)
	}
	return out, nil
}

// parseConfLines validates "key=value,key2=value2" into individual local.conf
// lines.
func parseConfLines(s string) ([]string, error) {
	parts, err := splitList("--conf", s)
	if err != nil || parts == nil {
		return nil, err
	}
	for _, p := range parts {
		key, _, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("--conf: %q must be key=value", p)
		}
	}
	return parts, nil
}
