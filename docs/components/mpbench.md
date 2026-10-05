# mpbench (end-to-end backup/restore benchmark)

A development tool that measures how a change to the backup or restore protocol (or to `brfs`,
`bwfs`, `rwfs`) moves throughput. It runs the **full cycle** against the real binaries — read files,
back them up, restore them, write them back, compare — over an emulated network, and reports
per-phase timings and wire bytes as a table and JSON.

It is a measuring stick, not a model of production: datasets are synthetic, the network model is a
fixed delay plus a bandwidth cap, and nothing here profiles CPU or memory. What it is good at is
being **repeatable**, so two builds can be compared fairly.

## Building

```bash
make build      # brfs, bwfs and rwfs (plus everything else)
make mpbench    # the benchmark itself, into bin/mpbench
```

## Usage

```bash
mpbench --bin-dir bin --files 500 --profile mixed --dup-ratio 0.3 \
        --rtt 50ms --bandwidth 100mbit --streams 4 --window 8 \
        --runs 3 --seed 1 --json out.json
```

| Flag | Default | Description |
|------|---------|-------------|
| `--bin-dir` | — | **Required.** Directory holding the `brfs`, `bwfs` and `rwfs` binaries to measure. All three come from here, so a protocol change is always measured with a matched client and server. |
| `--files` | 500 | Number of files in the generated dataset |
| `--profile` | `mixed` | Size mix: `small` (1–32 KB), `mixed` (70% 1–32 KB, 25% 64 KB–1 MB, 5% 2–8 MB), `large` (2–8 MB) |
| `--dup-ratio` | 0.3 | Fraction of full 64 KB blocks drawn from a small shared pool, so chunk-level deduplication is exercised within and across files (0–1) |
| `--seed` | 1 | Dataset seed. The same seed gives byte-identical data |
| `--rtt` | 0 | Emulated **round-trip** time, e.g. `50ms` (each direction is delayed by half) |
| `--bandwidth` | unlimited | Emulated bandwidth per direction: `100mbit`, `1gbit`, `10mbyte`, … (decimal units) |
| `--streams` | 4 | `--streams` passed to `brfs` and `rwfs` |
| `--window` | 0 | `--window` passed to `brfs`; `0` leaves `brfs`'s own default (see [brfs](brfs.md#sliding-window)) |
| `--brfs-args` | | Extra arguments for `brfs`, space separated |
| `--rwfs-args` | | Extra arguments for `rwfs restore`, space separated |
| `--runs` | 3 | Number of full cycles; the report gives median, min and max |
| `--json` | | Also write the full report to this path |
| `--keep` | false | Keep each run's work directory (logs, store, restored tree) for inspection |

Exit codes: `0` success, `1` a run failed (a subprocess failed or the restored tree differs from the
source), `2` usage error.

## What one run does

Every run uses a fresh temporary directory, a fresh store and a freshly generated dataset.

1. Generate the dataset from `--seed`.
2. Create a throwaway CA and one certificate carrying the `client` role, start `bwfs`, and put the
   delay proxy in front of it. `brfs` and `rwfs` connect through the proxy.
3. **`backup-cold`** — `brfs` into the empty store: every chunk's data is transferred. Stresses
   bandwidth and data handling.
4. **`backup-warm`** — every file's mtime is advanced, then `brfs` runs again. A file's ID embeds its
   mtime, so each file is sent again, but every chunk is already stored and only hashes cross the
   wire. This is the latency-bound case, where per-chunk round trips dominate.
5. **`restore`** — `rwfs restore` selects the whole source tree and writes it to another directory.
   Restore is a server-streaming RPC, so it is throughput-bound rather than latency-bound.
6. **Verify** — the SHA-256 of every source file is compared with its restored copy. Any difference
   fails the run, so a speedup is never reported on broken data.

## Reading the output

```
phase        median s  min s  max s  MB/s  files/s  wire up  wire down
```

`MB/s` and `files/s` are the dataset's logical size and file count divided by the phase's wall time.
`wire up` is bytes from the client to `bwfs`, `wire down` the reverse, counted by the proxy for that
phase only. Wire bytes show protocol-overhead changes (for example compression) that wall time alone
can hide: a change that moves less data but is not faster shows up here.

## Comparing two builds

```bash
# baseline: build the old code's binaries into base/bin, the new into new/bin
mpbench --bin-dir base/bin --files 300 --rtt 50ms --bandwidth 200mbit --seed 1 --json base.json
mpbench --bin-dir new/bin  --files 300 --rtt 50ms --bandwidth 200mbit --seed 1 --json new.json
```

Use **identical flags and the same `--seed`**, and compare the `summary` sections of the two JSON
files. The JSON also records the SHA-256 of the three binaries used, so a comparison states exactly
what was measured. Extra flags reach the real binaries, so a tuning flag needs no rebuild:

```bash
mpbench --bin-dir bin --rtt 50ms --window 1 --json w1.json
mpbench --bin-dir bin --rtt 50ms --window 8 --json w8.json
```

Run it with a non-zero `--rtt` when measuring anything latency-related: on loopback there is no
latency to hide, so a window or pipelining change shows nothing.

## Network emulation

A userspace TCP proxy between the clients and `bwfs`. It delays each direction by half of `--rtt`
using a timestamped queue — blocks that arrive together leave together — so it models propagation
delay without serializing a stream, which would hide exactly the pipelining gains being measured.
`--bandwidth` is a per-direction cap. TLS passes through untouched. It needs no root and works
anywhere Go runs. It does not model jitter or loss; use `tc netem` for those.

## See Also

- [brfs](brfs.md) — backup reader; the sliding window is what `--window` tunes
- [bwfs](bwfs.md) — the storage server being driven
- [rwfs](rwfs.md) — restore
- [Design: mpbench](../superpowers/specs/2026-10-05-mpbench-design.md)
