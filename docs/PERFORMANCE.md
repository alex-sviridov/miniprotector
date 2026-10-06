# Performance tuning

What can be adjusted to make backup and restore faster, what each knob does, and the values
measured with [mpbench](components/mpbench.md). Defaults are chosen from those measurements; this
page says when to move off them.

## Why backup and restore are fast or slow

- **Backup is a conversation.** For every chunk (64 KB) `brfs` asks `bwfs` "do you have this?", and
  for a new chunk sends the data and waits for the confirmation. Each question costs a network round
  trip, so on a link with latency the round trips, not the bandwidth, set the speed. Every file also
  costs a few round trips of its own (announce the file, wait for the result).
- **Restore is a stream.** `rwfs` asks for a file once and `bwfs` streams all its chunks back without
  waiting, so restore is much less sensitive to latency. It still pays a round trip or more per file.
- **On a fast network the limit moves to the machines:** hashing, writing chunk files and database
  rows on the server. More streams or a larger window do nothing there.

Rule of thumb: **latency makes things slow, parallelism hides it.** There are two kinds of
parallelism, and they fix different problems.

| Knob | Parallelism it adds | Fixes | Does nothing for |
|---|---|---|---|
| `--window` / `default_window` | chunks in flight **within one file**, per stream | big files, and runs where most chunks are already stored (only hashes cross the wire) | many small files (the window empties at every file boundary) |
| `--streams` / `default_streams` | files in flight **at once** | many small files; also helps big files and restore | zero-latency links (no latency to hide) |

## What to adjust

### `--streams` (config `default_streams`) — many streams for small files

Number of files `brfs` (backup) or `rwfs` (restore, verify) process concurrently. This is the knob
for **small-file workloads**: each small file is one chunk, so the window never fills and each file
costs about two round trips, regardless of window. More streams overlap those round trips.

Measured at 50 ms RTT, 200 Mbit/s cap, mixed dataset (120 files, 59.8 MB), window 16:

| streams | backup-cold | backup-warm | restore |
|---|---|---|---|
| 4 | 7.7 s | 5.3 s | 3.7 s |
| 8 | 4.7 s (−39%) | 3.3 s (−38%) | 3.1 s (−16%) |
| 16 | 4.2 s (−45%) | 2.8 s (−48%) | 3.1 s (−16%) |

On 200 small files (1–32 KB) at 50 ms, 4 streams cut every phase by 49% and 8 streams by 73%
against 2, close to linear. On a LAN (RTT 0) 4, 8 and 16 streams are the same within noise, so a higher value costs
nothing there.

- **Default:** `default_streams` is a required key in `local.conf`; the shipped demo config uses
  **8**. `rwfs` now follows the same key (it used to hard-code 4); `--streams` overrides either tool.
- **Raise it** for high-latency links and small files, up to about 16. Returns diminish quickly.
- **Cost:** each stream is a gRPC stream and an open, locked source file; each is also a goroutine
  on `bwfs`. Very high values mostly add contention.

### `--window` (config `default_window`) — chunks in flight per stream

How many chunks of one file a stream keeps in flight before waiting for replies (see
[brfs sliding window](components/brfs.md#sliding-window)). `1` is the old stop-and-wait behaviour.

Sizing: the window must cover the bandwidth-delay product, `RTT × throughput / 64 KB`. Measured at 8
streams, 50 ms RTT, 200 Mbit/s cap:

| window | backup-cold | backup-warm |
|---|---|---|
| 4 | 6.2 s | 3.9 s |
| 8 | 5.0 s (−19%) | 3.3 s (−16%) |
| 16 | 4.9 s (−21%) | 3.3 s (−16%) |
| 32 | 4.9 s (−21%) | 3.1 s (−21%) |

With one stream and no other parallelism the window is worth much more: at 50 ms RTT, `--window 8`
cut a 60 MB cold backup from 54 s to 17 s and a warm (hash-only) backup from 32 s to 11 s.

- **Default:** `default_window=16`, the knee in the measurements above. On a LAN, 8 vs 32 made no
  difference (+1%).
- **Raise it** for long, fast links (bandwidth × RTT large) and big files. **Lower it** (or use `1`)
  to limit memory: chunks held are `streams × window × 64 KB` (8 × 16 × 64 KB = 8 MB).
- A `bwfs` older than the sliding-window change requires `--window 1`.

### `grpc_window_bytes` (config) — HTTP/2 flow-control window, off by default

By default gRPC sizes its flow-control window dynamically. Setting `grpc_window_bytes` (in
`local.conf`, applied by `brfs`, `bwfs` and `rwfs`) fixes it, in bytes (65536 – 1073741824, `0` = the
default dynamic behaviour). It mostly helps **restore over a high-latency link with few streams**:
one stream, 100 ms RTT, 28.7 MB restore: 3.23 s default, **2.44 s with 4 MiB (−24%)**.

It is **off by default** because:

- With more streams the gain is smaller: at 8 streams, 50 ms and a 200 Mbit/s cap, 4 and 16 MiB gave
  2.7 s against 3.1 s by default (−12%).
- A fixed window smaller than the link's bandwidth-delay product is *slower* than the default (1 MiB
  at 100 ms, one stream: 4.41 s against 3.23 s; 1 MiB at 8 streams, 50 ms, 200 Mbit/s: 4.1 s against
  3.1 s), and setting any value turns off gRPC's automatic sizing.

Only set it after measuring: pick a value of at least `bandwidth × RTT` and confirm with
`mpbench --conf grpc_window_bytes=<n>`.

### Restore write tuning

`rwfs restore` writes each file to a hidden temp file and commits in batches (fsync, rename, directory
fsync), so a file only appears under its final name once durable and `--overwrite` never destroys the
old file early (see the [rwfs write contract](components/rwfs.md#write-contract)). Two config keys
control the batching: `restore_commit_files` (default 64, range 0-1024) and `restore_commit_bytes`
(default 64 MiB); `0` for either commits after every file. Measured with `mpbench --files 500 --runs 5
--seed 1 --cold-cache` on a 4-core virtual SSD (ext4), restore phase median: A = previous direct
write, B = defaults, C = per-file fsync + rename.

| Profile | A (direct) | B (batched) | C (per-file) | B vs A | B vs C |
|---|---|---|---|---|---|
| small (8.6 MB) | 0.60 s | 0.65 s | 1.20 s | +8% | 46% faster |
| mixed (172 MB) | 1.33 s | 1.42 s | 1.84 s | +7% | 23% faster |
| large (2.6 GB) | 11.50 s | 11.53 s | 12.27 s | +0.3% | 6% faster (within noise) |

The durability contract costs about 7-8% on small and mixed trees; batching recovers most of what
per-file fsync would cost. `fallocate` preallocation and the post-sync cache drop were each within
noise on this hardware (the cache drop is borderline: removing it was about 4.5% faster on large, but
the ranges touch), so they are kept for their non-throughput benefits (early `ENOSPC`, smaller page
cache footprint) and flagged to revisit. Leave the defaults unless you measure otherwise; see [the
design](superpowers/specs/2026-10-06-rwfs-write-tuning-design.md) for the full numbers.

### Things that are not knobs (yet)

- **Chunk size** is fixed at 64 KB (the protocol doc still says 512 KB; the code is authoritative).
  Larger chunks would mean fewer round trips and fewer database rows; tracked as a possible change.
- **Per-chunk server cost.** Even at zero latency, backup runs at about 12 MB/s against about 105
  MB/s for restore; the cost is per-chunk work on the backup path, not the network. See
  [issue #35](https://github.com/alex-sviridov/miniprotector/issues/35).
- **Pipelining across files** would let one stream hide the per-file round trips that streams hide
  today. See [issue #36](https://github.com/alex-sviridov/miniprotector/issues/36).

## Net effect of the shipped defaults

The code before the sliding window with the old shipped setting (4 streams, no window) against the
current defaults (8 streams, window 16), same dataset and seed (120 files, 59.8 MB, `mixed`), median
of 2 runs (numbers re-measured after the `mpbench` bandwidth-cap fix, see below):

| Network | Phase | Before | After | Change |
|---|---|---|---|---|
| 50 ms RTT, 200 Mbit/s | backup-cold | 27.1 s | 5.0 s | −82% |
| | backup-warm | 16.6 s | 3.3 s | −80% |
| | restore | 3.7 s | 3.1 s | −16% |
| LAN (RTT 0) | backup-cold | 3.6 s | 3.7 s | +1% (within the ±6% run-to-run spread seen on a LAN) |
| | backup-warm | 2.6 s | 2.7 s | +3% |
| | restore | 0.31 s | 0.26 s | −16% |

Bytes on the wire are unchanged: the gain is all latency hidden, none of it data saved.

## A note on the bandwidth cap

Until 2026-10-06 `mpbench`'s bandwidth cap delivered only about 57% of the configured rate (the pacer
lost its sleep overshoot on every block), so restore under a cap looked like it plateaued at 12–13 MB/s
whatever the streams or window. That was the benchmark, not the restore path: with no cap, restore
scaled normally (7.7 s at 1 stream, 1.8 s at 8, 1.4 s at 16 at 50 ms RTT). Capped results from before
that date are not comparable with current ones; the tables on this page were re-measured. Backup was
barely affected because it is latency-bound, not bandwidth-bound. A capped restore now reaches about
75–80% of the cap at 200 Mbit/s; the rest is connection setup, resolution and the last file's tail.

## How to measure your own link

```bash
make build mpbench
# does latency matter here? compare a LAN profile with your link's RTT
bin/mpbench --bin-dir bin --files 200 --profile small --sweep-rtt 0,20ms,80ms --window 16 --streams 8

# how many streams? (small files are the stream-hungry case)
bin/mpbench --bin-dir bin --files 200 --profile small --rtt 50ms --window 16 --sweep-streams 2,4,8,16

# where is the window knee? (big files and warm backups are the window-hungry case)
bin/mpbench --bin-dir bin --files 120 --profile mixed --rtt 50ms --bandwidth 200mbit --streams 8 --sweep-window 4,8,16,32

# does a fixed gRPC window help restore on your link?
bin/mpbench --bin-dir bin --files 6 --profile large --rtt 100ms --streams 1 --conf grpc_window_bytes=4194304
```

Use your real round-trip time (`ping`) for `--rtt` and your real bandwidth for `--bandwidth`; the
datasets are synthetic, so treat results as relative, not absolute. See
[mpbench](components/mpbench.md) for all options, and compare two builds with identical flags and the
same `--seed`.

## Quick reference

| Symptom | Try |
|---|---|
| Slow backup of many small files over WAN | raise `default_streams` (8 → 16) |
| Slow backup of big files, or warm backups, over WAN | raise `default_window` (16 → 32), check `--streams` |
| Slow restore over a high-latency link with few streams | more streams first; then `grpc_window_bytes` ≥ bandwidth × RTT |
| Backup slow even on a LAN | not network-bound; see issue #35 |
| Wondering whether source-file reading is the limit | Probably not: on a virtual SSD with a cold cache, `brfs` read-ahead, `O_NOATIME`/fadvise and hole skipping gave no measurable gain (see [the tuning spec](superpowers/specs/2026-10-06-brfs-read-path-tuning-design.md)); measure with `mpbench --cold-cache` first |
| High memory on `brfs` | lower `default_window` or `default_streams` (`streams × window × 64 KB`) |
