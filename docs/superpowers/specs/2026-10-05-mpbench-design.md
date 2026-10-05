# Design: mpbench — end-to-end backup/restore benchmark

## Purpose

A development tool that measures how a change to the backup or restore protocol (or to `brfs`,
`bwfs`, `rwfs`) moves throughput. It does not need to be precise or model real workloads; it needs
to be **repeatable**, to exercise the **full cycle** (read file → back it up → restore it → write it
back), and to make **latency-bound** gains visible (the `brfs --window` change is the motivating
case: it shows nothing on localhost).

Non-goals: CPU/memory profiling, concurrent clients, jitter or packet loss (use `tc netem` for
those), CI wiring, an in-tool before/after compare (a run produces JSON; compare two runs' JSON).

## How it is used

```
mpbench --bin-dir bin --files 500 --profile mixed --dup-ratio 0.3 \
        --rtt 50ms --bandwidth 100mbit --streams 4 --window 8 \
        --runs 3 --seed 1 --json out.json
```

To measure a change: build the baseline binaries, run `mpbench --bin-dir <baseline>/bin --json
base.json`; build the new ones, run again with **identical flags**, and diff the two JSON files.
`bwfs`, `brfs` and `rwfs` come from `--bin-dir` together, so a protocol change is always measured
with a matched client and server. Extra flags reach the real binaries through `--brfs-args` and
`--rwfs-args`, so tuning a flag (`--window 1` vs `8`) needs no rebuild.

## One run

Every run uses a fresh temp work directory, a fresh store and a freshly generated dataset.

1. **Dataset** — generated from `--seed` (deterministic, see below).
2. **Environment** — a throwaway CA and one leaf certificate carrying the `client` role, a
   `local.conf`, and `MP_CONFIG_PATH` pointing at them. `bwfs <store> server` is started on a free
   port; the delay proxy listens on another free port in front of it. Readiness is waited for by
   dialing the proxy, not by sleeping.
3. **`backup-cold`** — `brfs <src> --destination localhost:<proxy>`; the store is empty, so every
   chunk's data is transferred.
4. **`backup-warm`** — every source file's mtime is advanced by at least 2 seconds (`os.Chtimes`), then
   `brfs` runs again. A file's ID embeds its whole-second mtime, so this changes the ID, so each file is re-sent, but every chunk is already stored: only
   hashes cross the wire. This is the latency-bound case the window targets.
5. **`restore`** — `rwfs restore localhost:<proxy> --rules-stdin` with
   `{"rules":[{"host":"","path":"<src>","include":true,"dest_path":"<restored>"}]}`.
6. **Verify** — SHA-256 of every source file is compared with its restored counterpart (and the file
   sets must match). Any difference makes the run, and the tool, fail. A benchmark must not be able
   to report a speedup on broken data.

Each phase is timed as the wall clock of the subprocess. The report gives, per phase: seconds,
logical payload bytes (the dataset size), MB/s, files/s, and **wire bytes** in each direction as
counted by the proxy (reset at the start of each phase), so protocol-overhead changes such as
compression are visible and not only speed. A non-zero exit of any subprocess aborts the run with the
tail of its output.

## Units

`src/cmd/mpbench/`, `package main`, one file per unit:

| File | Responsibility | Interface |
|------|----------------|-----------|
| `args.go` | flags and validation | `parseArgs() (*Args, error)` |
| `dataset.go` | seeded dataset generation + tree comparison | `Generate(dir, DatasetSpec) (Dataset, error)`, `Dataset.Touch()`, `CompareTrees(src, dst) error` |
| `certs.go` | throwaway CA, role-bearing leaf, `local.conf` | `WriteIdentity(dir string) error` |
| `proxy.go` | TCP delay/bandwidth proxy with byte counters | `StartProxy(listen, target string, ProxySpec) (*Proxy, error)`, `Proxy.Counters()`, `Proxy.ResetCounters()`, `Proxy.Close()` |
| `runner.go` | environment lifecycle and the phases | `RunOnce(ctx, Args, runIndex) (RunResult, error)` |
| `report.go` | statistics, table, JSON | `Summarize([]RunResult) Summary`, `Summary.WriteTable(w)`, `WriteJSON(path, Report)` |
| `main.go` | wiring and exit code | — |

### Dataset

`--files N` (default 500), `--profile small|mixed|large` (default `mixed`), `--dup-ratio R`
(default 0.3), `--seed S` (default 1).

The profile sets size classes by file count: `small` = 1–32 KB; `mixed` = 70% 1–32 KB, 25%
64 KB–1 MB, 5% 2–8 MB; `large` = 2–8 MB. Files are placed in nested directories (depth ≤ 3). A file
is a sequence of 64 KB blocks (the chunk size) plus a partial tail. Each full block is, with
probability `--dup-ratio`, drawn from a small seeded pool of shared blocks (so chunk-level dedup is
exercised, within and across files), otherwise fresh seeded-random bytes. The generator uses a seeded
`math/rand/v2` PCG source only; the same seed gives byte-identical data, which is what makes two
runs comparable.

### Proxy

A userspace TCP proxy, one goroutine pair per connection, no root and no `tc`. `--rtt` is the full
round-trip time, so each direction is delayed by `rtt/2`. Delay is implemented with a **timestamped
queue**: the reader stamps every byte block with its arrival time and the writer releases it at
`arrival + delay`. It must not sleep per read, which would serialize the stream and flatten exactly
the pipelining gains being measured. `--bandwidth` is a per-direction token bucket (accepts
`100mbit`, `1gbit`, `10mbyte`; unset or `0` means unlimited). TLS is opaque to it. The proxy counts
bytes per direction.

### Certificates

`bwfs` enforces roles from an X.509 attribute extension (OID `1.3.6.1.4.1.61183.1.1`, JSON
`{"authz-role":"client"}`, see `common/mtls/peer.go`). `certs.go` generates a CA and one leaf with
SANs `bwfs.internal` and `localhost`, that extension, written as `certs/{ca.crt,client.crt,client.key}`
in the layout `common/mtls` loads. The same identity serves `bwfs`, `brfs` and `rwfs`.

### Report

Over `--runs` (default 3) the report gives, per phase, median, min and max of seconds and the
derived rates, and the median wire bytes. The table goes to stdout; `--json` also writes the full
record: tool version, flags, network profile, dataset stats (files, bytes, duplicate ratio, seed),
SHA-256 of the three binaries used (provenance for a comparison), every run's raw numbers, and the
summary. `--keep` keeps the work directory for inspection; otherwise it is removed.

## Errors and cleanup

`bwfs` and the proxy are always stopped, the work directory removed (unless `--keep`), and
subprocesses are bound to the tool's context so Ctrl-C leaves nothing running. A missing binary in
`--bin-dir` fails before anything starts. Invalid flags (non-positive counts, malformed `--rtt` or
`--bandwidth`) are rejected up front.

## Testing

- `dataset`: same seed → identical bytes; different seed → different; `--dup-ratio 0` yields no
  shared blocks and `1` yields only pool blocks; `Touch` changes mtimes but not content;
  `CompareTrees` detects a missing, extra and modified file.
- `proxy`: bytes pass through unchanged and in order; one round trip through it costs about the
  configured RTT; **N concurrent messages sent back-to-back arrive in about one delay, not N**
  (guards the non-serializing property); the bandwidth cap bounds throughput; counters are right.
- `certs`: generated identity is accepted by `mtls.LoadServerCredentials` / `LoadClientCredentials`
  and carries `authz-role=client`.
- `report`: median/min/max and rates over fixed inputs; JSON round-trips.
- `args`: size/duration/bandwidth parsing and rejection of bad values.
- One integration test (`-tags integration`) builds `brfs`, `bwfs`, `rwfs` into a temp dir, runs a tiny
  full cycle through `RunOnce`, and asserts verify passed, the warm backup sent far fewer wire bytes
  upstream than the cold one, and the restored tree equals the source.

## Integration with the repo

- `make mpbench` target (the Makefile has one explicit rule per binary; `build` depends on the
  wildcard of `src/cmd/*`, so a rule is required).
- `docs/components/mpbench.md`, a README mention in the component list/docs index, and a CHANGELOG
  entry. `docs/ARCHITECTURE.md` is unchanged: `mpbench` is not part of the runtime topology.
- No `.proto` change, so no protocol doc update.

## Out of scope / follow-ups

- A `mpbench compare a.json b.json` subcommand.
- `tc netem`-based runs for jitter and loss.
- Pipelining across file boundaries is not benchmarked specially; the `small` profile shows how much
  a window loses to per-file draining.
