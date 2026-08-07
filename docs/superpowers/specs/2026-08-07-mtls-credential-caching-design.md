# mTLS Credential Caching — Design

> Follow-on to the incident fix in `docs/superpowers/specs/2026-07-05-issuer-attribute-template-design.md`'s
> sibling investigation: `issuer`'s gRPC listener went silent for ~24h after its daily self-cert
> refresh, root-caused to a torn write between `client.crt` and `client.key` (fixed separately by
> writing them via staged temp files + adjacent renames). While root-causing that incident, a
> second, independent issue surfaced in the same code path: `common/mtls`'s `GetCertificate` /
> `GetClientCertificate` callbacks re-read and re-parse `client.crt`/`client.key` from disk on
> *every single TLS handshake* — not just at startup. This design replaces that with an in-memory
> cache, bounded by both a TTL and the certificate's own expiration.

## Problem

`src/common/mtls/mtls.go`'s `serverTLSConfigForTier` and `clientTLSConfigWithIdentity` each install
a callback (`GetCertificate` for inbound handshakes, `GetClientCertificate` for outbound dials) that
calls `loadIdentityCert`/`loadIdentityCertFiles` — a full `tls.LoadX509KeyPair` disk read and parse
— on every invocation. This was deliberate (see the removed comments: "same per-handshake
certificate reload every gRPC server's credentials already get"), so that a rotated identity is
picked up without a restart. But it means every connection pays a full file-read-and-parse cost, and
on any transient read error the handshake fails outright with no fallback — there's no cached
known-good identity to fall back to.

## Scope

**In scope:** `common/mtls`'s identity loading for both the server path (`GetCertificate`) and the
client path (`GetClientCertificate`) — they share the same underlying read pattern and the same
risk profile. This also covers `ServerTLSConfig`/`ClientTLSConfig` (used by `log-gateway`'s
`net/http`-based server/client) for free, since they call the same underlying functions.

**Out of scope:** the CA pool (`loadCAPool`/`ClientCAs`) — already loaded once at startup and held
statically in the `tls.Config`, never reloaded per-handshake, so it has no analogous problem.
Making the cache TTL configurable per-deployment — not asked for, YAGNI.

## Design

### Where writer and reader live

The cache must work correctly whether the process that rewrites `client.crt`/`client.key` is the
same process reading them, or a different one — both happen in this codebase:

- `issuer` mints and rewrites its own identity in a background goroutine within the same process
  that serves it (`cmd/issuer/main.go`).
- Every other component's operating identity is rewritten by `certclient operating-refresh`, a
  short-lived process `agent` execs on a schedule (`cmd/agent/policy.go`, every 15 minutes) — a
  different OS process from the long-running server/client that reads the files.

Since there's no in-process channel between separate OS processes, the cache can only ever detect a
change by observing the filesystem (stat/mtime), never by a push from the writer. One mechanism
that does this correctly serves both cases identically — no special-casing `issuer`.

### Cache type

A small, unexported type in `common/mtls`, one instance per identity-loading call site (one created
inside `serverTLSConfigForTier`, one inside `clientTLSConfigWithIdentity`), captured directly in the
existing closures — no global registry, no cross-identity key lookup, matching how `certsDir` is
already captured today.

```go
type cachedIdentity struct {
	certsDir, certFile, keyFile string
	now                         func() time.Time // time.Now in production; injectable for tests

	mu         sync.Mutex
	cert       tls.Certificate
	crtModTime time.Time
	keyModTime time.Time
	validUntil time.Time
}

func (c *cachedIdentity) Get() (tls.Certificate, error)
```

A plain `sync.Mutex` guards the whole check-and-maybe-reload sequence — simple and safe, and cheap
enough that lock contention is a non-concern at this system's connection volumes (a backup control
plane, not a high-QPS service). The win is eliminating file I/O on the common path, not shaving
nanoseconds off a lock. Holding the mutex across the (rare) reload means concurrent handshakes that
land exactly during a reload block briefly on each other rather than triggering duplicate reads —
an acceptable, bounded wait (one file read), not a busy retry loop.

### `Get()` algorithm

1. **Fast path.** `now() < validUntil` (expected true for the large majority of calls): return the
   cached `cert`. Zero I/O.
2. **TTL elapsed, check for an actual change.** `stat()` both files (cheap — no read, no parse).
   Neither mtime differs from what's cached: bump `validUntil = min(now()+60s, cert.Leaf.NotAfter)`
   and return the cached `cert` — still no reparse. This is the expected outcome the large majority
   of the time a TTL lapses, since certs only actually change roughly every 15 minutes
   (`operating-refresh`) to 24 hours (`issuer` self-refresh).
3. **mtime changed, or this is the very first call ever.** `tls.LoadX509KeyPair`. On success,
   replace `cert`/`crtModTime`/`keyModTime`/`validUntil` (same `min(60s, NotAfter)` rule — Go 1.26's
   `tls.LoadX509KeyPair` populates `Certificate.Leaf` automatically, so `NotAfter` needs no extra
   parsing) and return the new `cert`.
4. **Reload fails at step 3.**
   - A cached cert already exists: log the error, keep serving the cached `cert` (don't fail the
     live handshake), and leave `validUntil` unadvanced so the very next call retries — self-healing
     without an outage, the same "keep the last known-good identity" philosophy `mintSelfIdentity`'s
     own refresh ticker already uses.
   - No cached cert yet (first-ever call — server/client startup): propagate the error. Matches
     today's existing fail-fast-at-startup behavior (`serverTLSConfigForTier` already calls
     `loadIdentityCert` once before returning, specifically to fail at build time rather than on the
     first handshake).

The 60s constant is a deliberate, explicit tradeoff: a rotated identity is picked up within at most
60s instead of instantly, in exchange for eliminating a full disk read-and-parse from the hot path.
60s is a small fraction of both refresh intervals (15 min / 24h), so it has no practical effect on
this system's actual rotation cadence.

### Where it plugs in

- `serverTLSConfigForTier`: the `GetCertificate` closure's `loadIdentityCert(certsDir)` call becomes
  `cache.Get()`. The existing "fail fast at build time" call becomes the cache's first `Get()`,
  which also warms the cache before the first real handshake arrives.
- `clientTLSConfigWithIdentity`: `getClientCert`'s `loadIdentityCertFiles(...)` call becomes the
  same type's `Get()`, same warm-at-build-time treatment.

### Security properties

- Never serves a certificate past its own `NotAfter` — the TTL is capped at the certificate's actual
  remaining validity, not just a flat 60s, so an about-to-expire cert gets checked more eagerly than
  one with months of headroom left.
- Bounds staleness to ≤60s after a rotation — an explicit, reviewed tradeoff, not a silent one.
- Strictly safer than today on transient read errors: today, any `loadIdentityCert`/
  `loadIdentityCertFiles` failure inside `GetCertificate`/`GetClientCertificate` fails that
  handshake outright with no fallback. The cache instead falls back to the last known-good identity
  and retries on the next call — this also acts as a defense-in-depth backstop against the kind of
  torn-write race the sibling incident fix addressed at the source.
- Makes no assumption about writer/reader being the same process — only ever observes the
  filesystem, so it's correct for both `issuer`'s self-mint and every other component's
  externally-refreshed identity.

### Testing

Unit tests in `common/mtls`, with `now` injected so tests don't sleep:

- Cache hit within the TTL window does zero disk I/O (verify by removing/corrupting the underlying
  files after the first successful load and confirming `Get()` still returns the original cert).
- TTL elapsed but mtime unchanged: bumps `validUntil` without reparsing (verify via a call counter
  or by corrupting file *content* — not mtime — and confirming the corrupted content is never
  read).
- TTL elapsed and mtime changed: reloads and returns the new cert.
- A cert with less than 60s of remaining validity caps `validUntil` at `NotAfter`, not `now()+60s`.
- A reload failure with an existing cache falls back to the cached cert and leaves `validUntil`
  unadvanced (next call retries).
- A reload failure with no existing cache (first call) propagates the error.

### Documentation

- `docs/components/certclient.md`/`docs/components/issuer.md`: any prose describing "per-handshake"
  reload gets corrected to describe the cache and its bound.
- Doc comments on `ServerTLSConfig`/`ClientTLSConfig` in `mtls.go` that currently say "the same
  per-handshake certificate reload every gRPC server's credentials already get" get updated to
  describe the cache instead.
- `CHANGELOG.md`: new entry.

## Non-goals (explicit)

- Configurable TTL — fixed at 60s, not exposed via config.
- A background/proactive refresh (fsnotify watcher, timer goroutine) — the cache is purely
  reactive, checked on access, matching this system's connection volumes.
- Any change to `loadCAPool`/`ClientCAs` handling.
- Any change to the write side (`mintSelfIdentity`, `certclient operating-refresh`) — already
  addressed by the sibling torn-write fix.
