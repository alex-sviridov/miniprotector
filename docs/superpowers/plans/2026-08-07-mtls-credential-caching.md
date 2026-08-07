# mTLS Credential Caching Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `common/mtls`'s `GetCertificate`/`GetClientCertificate` callbacks from reading and
parsing `client.crt`/`client.key` from disk on every single TLS handshake; cache the parsed
identity in memory instead, bounded by a fixed TTL and the certificate's own expiration.

**Architecture:** A new unexported `cachedIdentity` type wraps the existing
`tls.LoadX509KeyPair`-based loading with a mutex-guarded, mtime-checked, expiry-capped cache. One
instance is created per identity-loading call site (`serverTLSConfigForTier`,
`clientTLSConfigWithIdentity`) and captured directly in the existing closures — no global registry.

**Tech Stack:** Go 1.26 (`tls.LoadX509KeyPair` auto-populates `Certificate.Leaf`, used for the
expiration cap), stdlib only (`sync`, `time`, `os`, `crypto/tls`) — no new dependencies.

## Global Constraints

- TTL is a fixed 60s constant, not configurable — see
  `docs/superpowers/specs/2026-08-07-mtls-credential-caching-design.md` Non-goals.
- No background goroutine or fsnotify watcher — purely reactive, checked on access.
- The cache must never serve a certificate past its own `NotAfter`.
- The cache must work identically whether the writer of `client.crt`/`client.key` is the same
  process (`issuer`'s self-mint) or a different one (`agent` execing `certclient
  operating-refresh`) — detection is filesystem-based (mtime) only, never a push from the writer.
- `loadCAPool`/`ClientCAs` handling is out of scope — already loaded once at startup, unchanged.
- All work happens inside `src/common/mtls/`; no other package's code changes.

---

### Task 0: Commit the already-completed torn-write fix

This plan's diffs will be cleaner to review if the working tree starts clean. A separate,
already-implemented-and-tested fix (from the preceding debugging session) is currently uncommitted:
`mintSelfIdentity` in `src/cmd/issuer/selfidentity.go` now stages `client.crt`/`client.key` into
temp files and commits both via adjacent renames, instead of two plain sequential `os.WriteFile`
calls that could leave them permanently mismatched if interrupted between the two writes (the root
cause of `issuer`'s gRPC listener going silent ~24h into uptime). This is unrelated to the caching
work below and should land as its own commit.

**Files:**
- Already modified: `src/cmd/issuer/selfidentity.go`
- Already modified: `src/cmd/issuer/selfidentity_test.go`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Confirm the working tree has exactly the expected uncommitted changes**

Run: `cd /home/alex/miniprotector && git status --short`
Expected: `M src/cmd/issuer/selfidentity.go` and `M src/cmd/issuer/selfidentity_test.go` (plus any
pre-existing unrelated untracked files already present before this session — leave those alone,
don't stage them).

- [ ] **Step 2: Re-run the issuer test suite to reconfirm the fix still passes**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/issuer/... -v`
Expected: all tests PASS, including `TestMintSelfIdentity_KeyWriteFailure_LeavesLiveFilesConsistent`.

- [ ] **Step 3: Add a CHANGELOG entry**

Add to the top of `/home/alex/miniprotector/CHANGELOG.md`, immediately after the `# Changelog`
header and its description line, before the existing most-recent entry:

```markdown
## 2026-08-07 — issuer: fix self-cert-refresh leaving a permanently mismatched identity

`issuer`'s daily self-cert-refresh wrote `client.crt` and `client.key` with two independent
`os.WriteFile` calls. Since `common/mtls` reloads both files from disk on every TLS handshake (not
just at startup), any interruption between the two writes — disk pressure, a permission error, the
process being killed mid-write — could leave `client.crt` holding the new certificate while
`client.key` still held the old private key, permanently failing every subsequent handshake until a
full restart. `mintSelfIdentity` now stages both files into temp files first and commits them via
two adjacent renames, so a failure while writing data never touches a live file.
```

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/issuer/selfidentity.go src/cmd/issuer/selfidentity_test.go CHANGELOG.md
git commit -m "$(cat <<'EOF'
fix(issuer): commit self-cert refresh's client.crt/client.key atomically

A torn write between the two files during the daily self-cert refresh
could leave them permanently mismatched, since mtls.go reloads both
from disk on every handshake. Stage both into temp files first, then
commit via two adjacent renames.
EOF
)"
```

---

### Task 1: `cachedIdentity` type with a full, clock-injectable unit test suite

Add the caching type in isolation, fully unit-tested with an injectable clock, before wiring it
into any production call site. This task's tests don't need real sleeps or TLS handshakes — they
call `Get()` directly.

**Files:**
- Modify: `src/common/mtls/mtls.go` (add `cachedIdentity` type after `loadIdentityCert`, i.e. after
  line 95)
- Modify: `src/common/mtls/mtls_test.go` (add test helper + test suite)

**Interfaces:**
- Produces: `func newCachedIdentity(certsDir, certFile, keyFile string) *cachedIdentity`, `func (c
  *cachedIdentity) Get() (tls.Certificate, error)`, field `c.now func() time.Time` (defaults to
  `time.Now`, directly settable by same-package tests via struct literal). Consumed by Task 2
  (`serverTLSConfigForTier`) and Task 3 (`clientTLSConfigWithIdentity`).

- [ ] **Step 1: Write the failing tests**

Add to `src/common/mtls/mtls_test.go`, after the existing `writeTestCertsDir` helper (after line
297):

```go
// writeSelfSignedIdentity writes a minimal, self-signed EC cert/key pair to
// certFile/keyFile inside dir, valid until notAfter. cachedIdentity's tests
// exercise Get() directly and never perform a real TLS handshake, so no CA
// chain is needed -- just a well-formed, parseable pair.
func writeSelfSignedIdentity(t *testing.T, dir, certFile, keyFile string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cache-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
}

func TestCachedIdentity_FirstGetLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cert, err := cache.Get()
	require.NoError(t, err)
	assert.NotEmpty(t, cert.Certificate)
}

func TestCachedIdentity_FirstLoadFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	_, err := cache.Get()
	assert.Error(t, err, "with no prior successful load, a load failure must propagate")
}

func TestCachedIdentity_WithinTTLServesFromMemoryWithoutDiskIO(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	// Corrupt the files on disk. If Get() touched disk again, this would
	// either error (corrupt content) or return a different certificate.
	require.NoError(t, os.WriteFile(filepath.Join(dir, identCertFile), []byte("not a cert"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, identKeyFile), []byte("not a key"), 0o600))

	fakeNow = fakeNow.Add(30 * time.Second) // still within the 60s TTL
	second, err := cache.Get()
	require.NoError(t, err)
	assert.Equal(t, first.Certificate, second.Certificate, "within the TTL window, Get() must not re-read disk")
}

func TestCachedIdentity_TTLElapsedButMtimeUnchanged_SkipsReparsing(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	crtPath := filepath.Join(dir, identCertFile)
	keyPath := filepath.Join(dir, identKeyFile)
	crtInfo, err := os.Stat(crtPath)
	require.NoError(t, err)
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)

	// Corrupt the content but restore the original mtimes -- proves the
	// unchanged-mtime path trusts mtime and genuinely skips reparsing,
	// rather than happening to reparse identical bytes back to the same
	// result. If it *did* reparse, this corrupted content would error.
	require.NoError(t, os.WriteFile(crtPath, []byte("not a cert"), 0o644))
	require.NoError(t, os.Chtimes(crtPath, crtInfo.ModTime(), crtInfo.ModTime()))
	require.NoError(t, os.WriteFile(keyPath, []byte("not a key"), 0o600))
	require.NoError(t, os.Chtimes(keyPath, keyInfo.ModTime(), keyInfo.ModTime()))

	fakeNow = fakeNow.Add(90 * time.Second) // past the 60s TTL
	second, err := cache.Get()
	require.NoError(t, err, "mtime unchanged, so Get() must not attempt to reparse the (corrupted) content")
	assert.Equal(t, first.Certificate, second.Certificate)
}

func TestCachedIdentity_MtimeChanged_Reloads(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(2*time.Hour))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future, future))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identKeyFile), future, future))

	fakeNow = fakeNow.Add(90 * time.Second)
	second, err := cache.Get()
	require.NoError(t, err)
	assert.NotEqual(t, first.Certificate, second.Certificate, "a genuinely rotated file must be picked up once the TTL has elapsed")
}

func TestCachedIdentity_TTLCappedByCertExpiration(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, start.Add(10*time.Second)) // expires well inside the 60s TTL

	fakeNow := start
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	// Rotate to a fresh, longer-lived cert, advancing the clock only 20s --
	// past the certificate's own 10s NotAfter, but well within a flat 60s
	// TTL. If validUntil were capped only at now()+60s, this would still
	// serve the already-expired cached cert.
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, start.Add(time.Hour))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future, future))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identKeyFile), future, future))

	fakeNow = start.Add(20 * time.Second)
	second, err := cache.Get()
	require.NoError(t, err)
	assert.NotEqual(t, first.Certificate, second.Certificate, "validUntil must be capped at the cached cert's own NotAfter, not just now()+60s")
}

func TestCachedIdentity_ReloadFailureWithExistingCache_FallsBackAndRetriesNextCall(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	// Corrupt content AND change mtime, so a reload is actually attempted
	// and fails.
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.WriteFile(filepath.Join(dir, identCertFile), []byte("not a cert"), 0o644))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future, future))

	fakeNow = fakeNow.Add(90 * time.Second)
	second, err := cache.Get()
	require.NoError(t, err, "a reload failure must fall back to the last known-good identity, not fail the caller")
	assert.Equal(t, first.Certificate, second.Certificate)

	// Restore a valid, genuinely different cert. Because validUntil was
	// left unadvanced by the failed reload, the very next call must retry
	// immediately rather than waiting out another TTL window.
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(2*time.Hour))
	future2 := time.Now().Add(2 * time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future2, future2))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identKeyFile), future2, future2))

	third, err := cache.Get() // fakeNow unchanged since the previous call
	require.NoError(t, err)
	assert.NotEqual(t, first.Certificate, third.Certificate, "a failed reload must not advance validUntil, so the next call retries immediately")
}
```

- [ ] **Step 2: Run the new tests to verify they fail (cachedIdentity doesn't exist yet)**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -run TestCachedIdentity -v`
Expected: FAIL to compile — `undefined: newCachedIdentity` (or `cachedIdentity`).

- [ ] **Step 3: Implement `cachedIdentity`**

Add to `src/common/mtls/mtls.go`, immediately after `loadIdentityCert` (after line 95, before
`loadCAPool`):

```go
// identityCacheTTL bounds how long cachedIdentity trusts an in-memory
// identity before re-checking disk. See
// docs/superpowers/specs/2026-08-07-mtls-credential-caching-design.md.
const identityCacheTTL = 60 * time.Second

// cachedIdentity caches a parsed tls.Certificate in memory, re-reading from
// disk only once its validity window has elapsed -- capped at
// identityCacheTTL or the certificate's own NotAfter, whichever is sooner --
// and even then only re-parsing if the underlying files' mtimes actually
// changed. This replaces a full disk read-and-parse on every single TLS
// handshake with, in the common case, a single mutex-guarded memory read.
//
// Works identically whether the process that rewrites certFile/keyFile is
// this same process (issuer's self-mint) or a different one (agent execing
// certclient operating-refresh) -- it only ever observes the filesystem,
// never assumes a push from the writer.
type cachedIdentity struct {
	certsDir, certFile, keyFile string
	now                         func() time.Time // time.Now in production; injectable for tests

	mu         sync.Mutex
	loaded     bool
	cert       tls.Certificate
	crtModTime time.Time
	keyModTime time.Time
	validUntil time.Time
}

func newCachedIdentity(certsDir, certFile, keyFile string) *cachedIdentity {
	return &cachedIdentity{
		certsDir: certsDir,
		certFile: certFile,
		keyFile:  keyFile,
		now:      time.Now,
	}
}

// Get returns the cached identity, refreshing it from disk first if the
// cache's validity window has elapsed. On a reload failure, it falls back
// to the last known-good identity (if any) rather than failing the caller,
// and deliberately leaves validUntil unadvanced so the very next call
// retries -- self-healing without ever failing a live handshake on a
// transient disk error.
func (c *cachedIdentity) Get() (tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.loaded && now.Before(c.validUntil) {
		return c.cert, nil
	}

	crtPath := filepath.Join(c.certsDir, c.certFile)
	keyPath := filepath.Join(c.certsDir, c.keyFile)

	if c.loaded {
		crtInfo, crtErr := os.Stat(crtPath)
		keyInfo, keyErr := os.Stat(keyPath)
		if crtErr == nil && keyErr == nil &&
			crtInfo.ModTime().Equal(c.crtModTime) && keyInfo.ModTime().Equal(c.keyModTime) {
			c.validUntil = c.nextValidUntil(now)
			return c.cert, nil
		}
	}

	cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		if c.loaded {
			return c.cert, nil
		}
		return tls.Certificate{}, err
	}
	crtInfo, err := os.Stat(crtPath)
	if err != nil {
		if c.loaded {
			return c.cert, nil
		}
		return tls.Certificate{}, err
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		if c.loaded {
			return c.cert, nil
		}
		return tls.Certificate{}, err
	}

	c.cert = cert
	c.crtModTime = crtInfo.ModTime()
	c.keyModTime = keyInfo.ModTime()
	c.loaded = true
	c.validUntil = c.nextValidUntil(now)
	return c.cert, nil
}

// nextValidUntil caps the cache's validity window at c.cert's own
// expiration, so a near-expiry certificate gets re-checked sooner than a
// fresh one -- never serving a certificate past its own NotAfter.
func (c *cachedIdentity) nextValidUntil(now time.Time) time.Time {
	ttl := now.Add(identityCacheTTL)
	if c.cert.Leaf != nil && c.cert.Leaf.NotAfter.Before(ttl) {
		return c.cert.Leaf.NotAfter
	}
	return ttl
}
```

Add `"sync"` and `"time"` to `mtls.go`'s import block (`os` is already imported).

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -run TestCachedIdentity -v`
Expected: all 7 `TestCachedIdentity_*` tests PASS.

- [ ] **Step 5: Run the full mtls package suite to confirm no regressions**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -v`
Expected: all tests PASS (existing tests are untouched by this task — `cachedIdentity` isn't wired
into any production path yet).

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add src/common/mtls/mtls.go src/common/mtls/mtls_test.go
git commit -m "$(cat <<'EOF'
feat(mtls): add cachedIdentity, a TTL- and expiry-bounded credential cache

Not yet wired into any production call site -- this is the isolated,
fully unit-tested caching primitive. See
docs/superpowers/specs/2026-08-07-mtls-credential-caching-design.md.
EOF
)"
```

---

### Task 2: Wire the cache into the server path (`GetCertificate`)

**Files:**
- Modify: `src/common/mtls/mtls.go:127-149` (`serverTLSConfigForTier`), `:250-258`
  (`ServerTLSConfig` doc comment)
- Modify: `src/common/mtls/mtls_test.go:158-181`
  (`TestServerTLSConfig_ReloadsCertificateOnEachNewConnection`)

**Interfaces:**
- Consumes: `newCachedIdentity(certsDir, certFile, keyFile string) *cachedIdentity`, `(*cachedIdentity).Get() (tls.Certificate, error)` from Task 1.

- [ ] **Step 1: Replace the failing-first assertion — rewrite the existing reload test**

Replace `TestServerTLSConfig_ReloadsCertificateOnEachNewConnection` (`src/common/mtls/mtls_test.go`
lines 158-181) with:

```go
func TestServerTLSConfig_CachesCertificateWithinTTL(t *testing.T) {
	dir := copyCertsDir(t, fixtureCertsDir)
	addr := startTestServer(t, dir)

	clientCfg, err := clientTLSConfig(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)

	// Baseline: valid cert on disk, handshake succeeds and warms the cache.
	require.NoError(t, dial(addr, clientCfg))

	// Corrupt the server's identity cert on disk without restarting the
	// listener. Before caching, GetCertificate re-read on every handshake
	// and this dial would now fail; the cache is still within its TTL
	// window, so it serves the in-memory copy instead of touching disk.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), []byte("not a key"), 0o600))
	assert.NoError(t, dial(addr, clientCfg), "a corrupted file within the cache TTL must not affect a live handshake")
}
```

- [ ] **Step 2: Run it to verify it currently fails**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -run TestServerTLSConfig_CachesCertificateWithinTTL -v`
Expected: FAIL — the second `dial` returns an error today, since `GetCertificate` still re-reads
the (now corrupted) file on every handshake.

- [ ] **Step 3: Wire `cachedIdentity` into `serverTLSConfigForTier`**

In `src/common/mtls/mtls.go`, replace `serverTLSConfigForTier` (lines 127-149) with:

```go
// serverTLSConfigForTier is serverTLSConfig, parameterized on which
// credential tier the listener accepts from its peers.
func serverTLSConfigForTier(certsDir string, tier requiredTier) (*tls.Config, error) {
	cache := newCachedIdentity(certsDir, identCertFile, identKeyFile)
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first handshake. This also warms the cache.
	if _, err := cache.Get(); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := cache.Get()
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
		ClientCAs:             caPool,
		ClientAuth:            tls.RequireAndVerifyClientCert,
		VerifyPeerCertificate: verifyPeerTier(tier),
	}, nil
}
```

- [ ] **Step 4: Update `ServerTLSConfig`'s doc comment**

In `src/common/mtls/mtls.go`, in the `ServerTLSConfig` doc comment (currently lines 250-255),
replace:

```go
// ServerTLSConfig returns the raw operating-tier *tls.Config
// LoadServerCredentials wraps into gRPC transport credentials -- for a
// server built directly on net/http.Server (like log-gateway) instead of
// gRPC. Same tier enforcement (rejects a bootstrap/issuer-caller peer
// cert) and the same per-handshake certificate reload every gRPC server's
// credentials already get from serverTLSConfigForTier.
```

with:

```go
// ServerTLSConfig returns the raw operating-tier *tls.Config
// LoadServerCredentials wraps into gRPC transport credentials -- for a
// server built directly on net/http.Server (like log-gateway) instead of
// gRPC. Same tier enforcement (rejects a bootstrap/issuer-caller peer
// cert) and the same cached, TTL-and-expiry-bounded certificate reload
// every gRPC server's credentials already get from serverTLSConfigForTier
// -- see cachedIdentity.
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -run TestServerTLSConfig_CachesCertificateWithinTTL -v`
Expected: PASS.

- [ ] **Step 6: Run the full mtls package suite**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -v`
Expected: all tests PASS.

- [ ] **Step 7: Run the full repo build and every package that transitively depends on `common/mtls`**

Run: `cd /home/alex/miniprotector/src && go build ./... && go test ./...`
Expected: build succeeds; all tests PASS (this exercises every server that calls
`LoadServerCredentials`/`LoadIssuerServerCredentials`/`ServerTLSConfig` — `bwfs`, `brfs`, `rwfs`,
`catalog`, `issuer`, `log-gateway`, etc. — none of which change behavior from their own
perspective, only from `common/mtls`'s internals).

- [ ] **Step 8: Commit**

```bash
cd /home/alex/miniprotector
git add src/common/mtls/mtls.go src/common/mtls/mtls_test.go
git commit -m "$(cat <<'EOF'
perf(mtls): cache the server-side identity cert instead of re-reading per handshake

GetCertificate now goes through cachedIdentity (added in the previous
commit) instead of a full tls.LoadX509KeyPair on every single TLS
handshake.
EOF
)"
```

---

### Task 3: Wire the cache into the client path (`GetClientCertificate`)

**Files:**
- Modify: `src/common/mtls/mtls.go:187-217` (`clientTLSConfigWithIdentity`), `:260-269`
  (`ClientTLSConfig` doc comment)
- Modify: `src/common/mtls/mtls_test.go:183-203`
  (`TestClientTLSConfig_ReloadsCertificateOnEachNewConnection`), `:415-420`
  (`TestClientTLSConfig_Success`'s stale assertion message)

**Interfaces:**
- Consumes: same as Task 2 — `newCachedIdentity`, `(*cachedIdentity).Get()` from Task 1.

- [ ] **Step 1: Rewrite the existing client-side reload test**

Replace `TestClientTLSConfig_ReloadsCertificateOnEachNewConnection` (`src/common/mtls/mtls_test.go`
lines 183-203) with:

```go
func TestClientTLSConfig_CachesCertificateWithinTTL(t *testing.T) {
	dir := copyCertsDir(t, fixtureCertsDir)
	cfg, err := clientTLSConfig(dir, "bwfs.internal")
	require.NoError(t, err)

	addr := startTestServer(t, fixtureCertsDir)

	// Baseline succeeds and warms the cache.
	require.NoError(t, dial(addr, cfg))

	// Corrupt the client's identity cert on disk. Before caching,
	// GetClientCertificate re-read on every dial and this would now fail;
	// the cache is still within its TTL window, so it presents the
	// in-memory copy instead.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), []byte("not a key"), 0o600))
	assert.NoError(t, dial(addr, cfg), "a corrupted file within the cache TTL must not affect a live dial")
}
```

- [ ] **Step 2: Update the stale assertion message in `TestClientTLSConfig_Success`**

In `src/common/mtls/mtls_test.go`, in `TestClientTLSConfig_Success` (around line 419), replace:

```go
	assert.NotNil(t, cfg.GetClientCertificate, "must present this node's identity via GetClientCertificate for cert-reload-on-handshake, same as clientTLSConfig")
```

with:

```go
	assert.NotNil(t, cfg.GetClientCertificate, "must present this node's identity via GetClientCertificate, same as clientTLSConfig")
```

- [ ] **Step 3: Run the new test to verify it currently fails**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -run TestClientTLSConfig_CachesCertificateWithinTTL -v`
Expected: FAIL — `GetClientCertificate` still re-reads the (now corrupted) file on every dial.

- [ ] **Step 4: Wire `cachedIdentity` into `clientTLSConfigWithIdentity`**

In `src/common/mtls/mtls.go`, replace `clientTLSConfigWithIdentity` (lines 187-217) with:

```go
func clientTLSConfigWithIdentity(certsDir, certFile, keyFile, host string) (*tls.Config, error) {
	cache := newCachedIdentity(certsDir, certFile, keyFile)
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first dial. This also warms the cache.
	if _, err := cache.Get(); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}

	getClientCert := func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		cert, err := cache.Get()
		if err != nil {
			return nil, err
		}
		return &cert, nil
	}

	if isLoopbackHost(host) {
		return &tls.Config{
			GetClientCertificate:  getClientCert,
			InsecureSkipVerify:    true, // hostname check disabled; chain is still verified below
			VerifyPeerCertificate: verifyChainOnly(caPool),
		}, nil
	}

	return &tls.Config{
		GetClientCertificate: getClientCert,
		RootCAs:              caPool,
		ServerName:           host,
	}, nil
}
```

- [ ] **Step 5: Update `ClientTLSConfig`'s doc comment**

In `src/common/mtls/mtls.go`, in the `ClientTLSConfig` doc comment (currently lines 260-266),
replace:

```go
// ClientTLSConfig returns the raw operating-tier *tls.Config
// LoadClientCredentials wraps into gRPC transport credentials -- for an
// HTTP client built directly on net/http (e.g. api-server dialing
// log-gateway's query_range proxy route) instead of gRPC. Presents the
// standard client.crt/client.key identity; same hostname/chain
// verification rules as LoadClientCredentials, including per-handshake
// certificate reload via GetClientCertificate.
```

with:

```go
// ClientTLSConfig returns the raw operating-tier *tls.Config
// LoadClientCredentials wraps into gRPC transport credentials -- for an
// HTTP client built directly on net/http (e.g. api-server dialing
// log-gateway's query_range proxy route) instead of gRPC. Presents the
// standard client.crt/client.key identity; same hostname/chain
// verification rules as LoadClientCredentials, including the same cached,
// TTL-and-expiry-bounded certificate reload via GetClientCertificate -- see
// cachedIdentity.
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -run TestClientTLSConfig_CachesCertificateWithinTTL -v`
Expected: PASS.

- [ ] **Step 7: Run the full mtls package suite**

Run: `cd /home/alex/miniprotector/src && go test ./common/mtls/... -v`
Expected: all tests PASS.

- [ ] **Step 8: Run the full repo build and test suite**

Run: `cd /home/alex/miniprotector/src && go build ./... && go vet ./... && go test ./...`
Expected: build succeeds; `go vet` shows only the pre-existing unrelated `cmd/brfs` warning (not
introduced by this work); all tests PASS.

- [ ] **Step 9: Commit**

```bash
cd /home/alex/miniprotector
git add src/common/mtls/mtls.go src/common/mtls/mtls_test.go
git commit -m "$(cat <<'EOF'
perf(mtls): cache the client-side identity cert instead of re-reading per dial

GetClientCertificate now goes through cachedIdentity instead of a full
tls.LoadX509KeyPair on every single outbound dial, mirroring the
server-side change in the previous commit.
EOF
)"
```

---

### Task 4: Changelog and final verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a CHANGELOG entry**

Add to the top of `/home/alex/miniprotector/CHANGELOG.md`, immediately after the `# Changelog`
header and its description line, before the existing most-recent entry (which by now is the Task 0
entry from this same plan):

```markdown
## 2026-08-07 — mtls: cache identity certificates instead of re-reading per connection

`common/mtls`'s `GetCertificate`/`GetClientCertificate` callbacks read and parsed
`client.crt`/`client.key` from disk on every single TLS handshake and outbound dial. Both now go
through a small in-memory cache (`cachedIdentity`) bounded by a fixed 60s TTL and the certificate's
own expiration, re-reading from disk only when that window has elapsed and the underlying files'
mtimes actually changed. A reload failure now falls back to the last known-good identity instead of
failing the live handshake outright, retrying on the next call. See
`docs/superpowers/specs/2026-08-07-mtls-credential-caching-design.md`.
```

- [ ] **Step 2: Full repo verification**

Run: `cd /home/alex/miniprotector/src && go build ./... && go vet ./... && gofmt -l common/mtls/ && go test ./...`
Expected: build succeeds; `go vet` shows only the pre-existing unrelated `cmd/brfs` warning;
`gofmt -l` prints nothing (no formatting issues); all tests PASS.

- [ ] **Step 3: Commit**

```bash
cd /home/alex/miniprotector
git add CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: add changelog entry for mtls credential caching
EOF
)"
```
