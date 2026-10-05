package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"
)

const toolVersion = "1"

type PhaseResult struct {
	Name         string  `json:"name"`
	Seconds      float64 `json:"seconds"`
	PayloadBytes int64   `json:"payload_bytes"`
	Files        int     `json:"files"`
	MBPerSec     float64 `json:"mb_per_s"`
	FilesPerSec  float64 `json:"files_per_s"`
	WireUp       int64   `json:"wire_up_bytes"`   // client -> server
	WireDown     int64   `json:"wire_down_bytes"` // server -> client
}

type RunResult struct {
	Index        int           `json:"index"`
	DatasetFiles int           `json:"dataset_files"`
	DatasetBytes int64         `json:"dataset_bytes"`
	Phases       []PhaseResult `json:"phases"`
}

func newPhaseResult(name string, seconds float64, payloadBytes int64, files int, wireUp, wireDown int64) PhaseResult {
	p := PhaseResult{Name: name, Seconds: seconds, PayloadBytes: payloadBytes, Files: files, WireUp: wireUp, WireDown: wireDown}
	if seconds > 0 {
		p.MBPerSec = float64(payloadBytes) / 1e6 / seconds
		p.FilesPerSec = float64(files) / seconds
	}
	return p
}

type PhaseSummary struct {
	Name              string  `json:"name"`
	Runs              int     `json:"runs"`
	PayloadBytes      int64   `json:"payload_bytes"`
	MedianSeconds     float64 `json:"median_seconds"`
	MinSeconds        float64 `json:"min_seconds"`
	MaxSeconds        float64 `json:"max_seconds"`
	MedianMBPerSec    float64 `json:"median_mb_per_s"`
	MedianFilesPerSec float64 `json:"median_files_per_s"`
	MedianWireUp      int64   `json:"median_wire_up_bytes"`
	MedianWireDown    int64   `json:"median_wire_down_bytes"`
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// Summarize reduces the runs to one summary per phase, in the first run's
// phase order.
func Summarize(runs []RunResult) []PhaseSummary {
	if len(runs) == 0 {
		return nil
	}
	var out []PhaseSummary
	for i, first := range runs[0].Phases {
		var secs, mb, fps, up, down []float64
		for _, r := range runs {
			if i >= len(r.Phases) {
				continue
			}
			p := r.Phases[i]
			secs = append(secs, p.Seconds)
			mb = append(mb, p.MBPerSec)
			fps = append(fps, p.FilesPerSec)
			up = append(up, float64(p.WireUp))
			down = append(down, float64(p.WireDown))
		}
		sortedSecs := append([]float64(nil), secs...)
		sort.Float64s(sortedSecs)
		out = append(out, PhaseSummary{
			Name:              first.Name,
			Runs:              len(secs),
			PayloadBytes:      first.PayloadBytes,
			MedianSeconds:     median(secs),
			MinSeconds:        sortedSecs[0],
			MaxSeconds:        sortedSecs[len(sortedSecs)-1],
			MedianMBPerSec:    median(mb),
			MedianFilesPerSec: median(fps),
			MedianWireUp:      int64(median(up)),
			MedianWireDown:    int64(median(down)),
		})
	}
	return out
}

func humanBytes(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func WriteTable(w io.Writer, header string, s []PhaseSummary) {
	fmt.Fprintln(w, header)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "phase\tmedian s\tmin s\tmax s\tMB/s\tfiles/s\twire up\twire down")
	for _, p := range s {
		fmt.Fprintf(tw, "%s\t%.2f\t%.2f\t%.2f\t%.2f\t%.1f\t%s\t%s\n",
			p.Name, p.MedianSeconds, p.MinSeconds, p.MaxSeconds, p.MedianMBPerSec, p.MedianFilesPerSec,
			humanBytes(p.MedianWireUp), humanBytes(p.MedianWireDown))
	}
	tw.Flush()
}

type ReportConfig struct {
	Files              int      `json:"files"`
	Profile            string   `json:"profile"`
	DupRatio           float64  `json:"dup_ratio"`
	Seed               uint64   `json:"seed"`
	RTTMillis          float64  `json:"rtt_ms"`
	BandwidthBytesPerS int64    `json:"bandwidth_bytes_per_s"`
	Streams            int      `json:"streams"`
	Window             int      `json:"window"`
	BrfsArgs           []string `json:"brfs_args"`
	RwfsArgs           []string `json:"rwfs_args"`
	Runs               int      `json:"runs"`
}

type ReportDataset struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

type Report struct {
	Tool      string            `json:"tool"`
	StartedAt time.Time         `json:"started_at"`
	Config    ReportConfig      `json:"config"`
	Dataset   ReportDataset     `json:"dataset"`
	Binaries  map[string]string `json:"binaries_sha256"`
	Runs      []RunResult       `json:"runs"`
	Summary   []PhaseSummary    `json:"summary"`
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// BuildReport assembles the JSON record, including the SHA-256 of the three
// binaries used so a comparison can show what was actually measured.
func BuildReport(a *Args, started time.Time, runs []RunResult, sum []PhaseSummary) (Report, error) {
	bins := map[string]string{}
	for _, name := range []string{"brfs", "bwfs", "rwfs"} {
		h, err := sha256File(filepath.Join(a.BinDir, name))
		if err != nil {
			return Report{}, fmt.Errorf("hash %s: %w", name, err)
		}
		bins[name] = h
	}
	rep := Report{
		Tool:      "mpbench " + toolVersion,
		StartedAt: started,
		Config: ReportConfig{
			Files: a.Files, Profile: a.Profile, DupRatio: a.DupRatio, Seed: a.Seed,
			RTTMillis: float64(a.RTT) / float64(time.Millisecond), BandwidthBytesPerS: a.Bandwidth,
			Streams: a.Streams, Window: a.Window, BrfsArgs: a.BrfsArgs, RwfsArgs: a.RwfsArgs, Runs: a.Runs,
		},
		Binaries: bins,
		Runs:     runs,
		Summary:  sum,
	}
	if len(runs) > 0 {
		rep.Dataset = ReportDataset{Files: runs[0].DatasetFiles, Bytes: runs[0].DatasetBytes}
	}
	return rep, nil
}

func WriteJSON(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
