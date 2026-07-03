# Control-Plane Compose Consolidation — Design

## Problem

`ca/` (step-ca) and `catalog/` are each independently deployable stacks today, sitting at the
repo root with their own `Dockerfile`/off-the-shelf image, `entrypoint.sh`, `docker-compose.yml`,
`local.conf`, and `README.md`. Architecturally they're already documented as one unit — the
"Control Plane" row in `docs/ARCHITECTURE.md` groups `ca/`, `certrequest`, and `catalog` together
— but there's no single command to bring both up together, and having two component-flavored
directories at the repo root (alongside `src/`, `docs/`) mixes deployment packaging with source
layout.

## Goals

- One `docker-compose.yml` starts both `ca` and `catalog` as separate containers on a single host.
- The same file still supports starting either service alone (`docker compose up ca` /
  `up catalog`), so a future split across two hosts needs no second compose file.
- Deployment artifacts for the control plane move out of the repo root into a dedicated location,
  with each service in its own subfolder.
- A single `make control-plane-up` target initializes (CA password generation, if missing) and
  brings the stack up, without trying to automate the token-relay step that's intentionally manual.

## Non-Goals

- No automation of the `certrequest` → `MP_CERT_TOKEN` → `certclient` enrollment flow — minting
  and relaying a token stays a manual, out-of-band step (existing rationale: automating it means
  either committing a secret or inventing a new secret-distribution mechanism).
- No changes to the `certrequest`/`certclient`/mTLS protocol or verification logic.
- No compose packaging for agent-side components (`bwfs`, `brfs`, `rwfs`) — out of scope; agents
  remain bare binaries today.
- No retroactive edits to historical spec/plan docs under `docs/superpowers/` that mention the old
  `ca/`/`catalog/` paths — those are point-in-time records, not living documentation.

## Directory Layout

```
deploy/
  control-plane/
    docker-compose.yml        # services: ca, catalog
    README.md                 # merges ca/README.md + catalog/README.md, adds combined
                               # quickstart + agent-enrollment flow
    .gitignore                 # */data/
    ca/
      entrypoint.sh            # was ca/entrypoint.sh
      data/                    # was ca/data (gitignored)
    catalog/
      Dockerfile               # was catalog/Dockerfile
      entrypoint.sh             # was catalog/entrypoint.sh
      local.conf                # was catalog/local.conf
      data/                      # was catalog/data (gitignored)
```

`ca/` and `catalog/` are removed from the repo root entirely (`git mv` for tracked files; `data/`
directories are gitignored today and are simply recreated fresh under the new paths).

## Compose File

```yaml
services:
  ca:
    image: smallstep/step-ca
    volumes:
      - ./ca/data:/home/step
      - ./ca/entrypoint.sh:/home/step/entrypoint.sh:ro
    ports:
      - "9000:9000"
    entrypoint: ["/home/step/entrypoint.sh"]
    restart: unless-stopped

  catalog:
    build:
      context: ../..
      dockerfile: deploy/control-plane/catalog/Dockerfile
    depends_on:
      - ca
    volumes:
      - ./catalog/data:/data
      - ./catalog/local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    ports:
      - "15723:15723"
    restart: unless-stopped
```

`context: ../..` resolves to the repo root (two levels up from `deploy/control-plane/`);
`dockerfile:` paths are resolved relative to that context, so `catalog/Dockerfile`'s
`COPY catalog/entrypoint.sh ./entrypoint.sh` becomes
`COPY deploy/control-plane/catalog/entrypoint.sh ./entrypoint.sh`.

`depends_on` only orders container start when both are brought up together on one host — it does
not substitute for the manual token-minting step catalog still needs before its first successful
boot. Each service remains independently startable (`docker compose up -d ca` or
`up -d catalog`), which is how a future split deployment uses this same file: run it on host A
with only `ca` started, host B with only `catalog` started, and point `catalog/local.conf`'s
`ca_host` at host A.

## Code and Documentation Updates

**`src/cmd/certrequest/arguments.go`** — default flag values move to the new nested paths:
- `--defaults-file` → `deploy/control-plane/ca/data/config/defaults.json`
- `--root` → `deploy/control-plane/ca/data/certs/root_ca.crt`
- `--password-file` → `deploy/control-plane/ca/data/secrets/password`

`certrequest` is still run from the repo root, as documented today — only the defaults change.

**Docs updated** (living docs only; historical `docs/superpowers/specs|plans/*` are left as-is):
- `docs/ARCHITECTURE.md` — `ca/` (step-ca container) → `deploy/control-plane/ca/`
- `docs/components/certrequest.md` — the three default-path table rows, plus the
  `ca/README.md` link → `deploy/control-plane/README.md`
- `docs/components/catalog.md` — `catalog/README.md` link → `deploy/control-plane/README.md`
- `docs/components/bwfs.md` — `ca/` step-ca setup reference → `deploy/control-plane/`
- `src/cmd/certrequest/e2e_test.go` — a comment mentioning `ca/data/` as an example path is
  updated for accuracy (not functionally load-bearing; the test uses `t.TempDir()` and explicit
  flags)

`ca/README.md` and `catalog/README.md` are deleted, replaced by one
`deploy/control-plane/README.md`.

## `make control-plane-up`

```makefile
CONTROL_PLANE_DIR := deploy/control-plane

control-plane-up: ## Initialize (if needed) and start the control-plane stack (ca + catalog)
	@if [ ! -f $(CONTROL_PLANE_DIR)/ca/data/secrets/password ]; then \
		echo -e "$(BLUE)Generating CA provisioner password...$(NC)"; \
		mkdir -p $(CONTROL_PLANE_DIR)/ca/data/secrets; \
		openssl rand -base64 32 > $(CONTROL_PLANE_DIR)/ca/data/secrets/password; \
	fi
	@cd $(CONTROL_PLANE_DIR) && docker compose up -d
	@echo -e "$(GREEN)Control plane up.$(NC) ca: https://localhost:9000  catalog: localhost:15723"
```

Idempotent: only generates the CA password if it's missing; otherwise just runs
`docker compose up -d` again. On a true first run, `ca` comes up fine but `catalog` crash-loops
under `restart: unless-stopped` until a token is minted and supplied — re-running with
`MP_CERT_TOKEN=<token> make control-plane-up` recreates the `catalog` container with the token
set, same as today's standalone `catalog/README.md` flow, now reachable through one target.

## `deploy/control-plane/README.md` Outline

1. **Overview** — combined `ca` + `catalog` control-plane stack; one compose file, independently
   startable services.
2. **First-time setup** — `make control-plane-up` (generates the CA password automatically, then
   starts both); mint catalog's token against `localhost`
   (`certrequest catalog --ca-url https://localhost:9000`, no `--san` needed) since hostname
   verification is skipped for loopback; `MP_CERT_TOKEN=<token> make control-plane-up` for
   catalog's first successful boot.
3. **Running** — `make control-plane-up` as the primary path; raw
   `docker compose up -d [ca|catalog]` documented underneath as what it does, for partial/manual
   control.
4. **Enrolling and connecting an agent (`bwfs`/`brfs`) node** — mint a token for the agent's real
   hostname (`certrequest <agent-host> --san <agent-host> --ca-url https://<this-host>:9000`),
   relay out-of-band, `MP_CERT_TOKEN=<token> certclient` on the agent; set the agent's
   `local.conf`: `ca_host=<this-host>:9000`, and `catalog_host=<this-host>:15723` if it runs
   `catalogsync`. Explicit callout: for any non-`localhost` `catalog_host`, the SAN minted for
   catalog **must exactly match** that value — Go's standard TLS hostname verification applies to
   every non-loopback host (only `localhost`/loopback skips the SAN check, per
   `common/mtls.LoadClientCredentials`), so the placeholder-name approach from the old
   `catalog/README.md` only works if the SAN is kept in sync with `catalog_host`.
5. **Certificate renewal** — same content as today's two READMEs (re-run `certclient` inside the
   container, or restart it periodically; `catalog`/`ca` pick up renewed certs on next connection
   without a restart).

## Testing

- `make build` / `make lint` — confirms the `certrequest` default-path change compiles and
  `go vet` is clean.
- `go test ./...` (specifically `src/cmd/certrequest`) — confirms existing unit/e2e tests, which
  already pass explicit path flags rather than relying on defaults, are unaffected.
- Manual smoke test (compose/deploy correctness isn't covered by Go tests):
  1. `make control-plane-up` from repo root — confirms password generation and `ca` startup.
  2. `certrequest catalog --ca-url https://localhost:9000` from repo root — confirms the new
     default paths resolve correctly.
  3. `MP_CERT_TOKEN=<token> make control-plane-up` — confirms the build context/Dockerfile path
     change works and `catalog` enrolls successfully.
  4. `make control-plane-up` again — confirms idempotent restart of an already-running stack.
  5. Point a throwaway `bwfs`/`catalogsync` `local.conf` at this stack's `ca_host`/`catalog_host`
     and confirm enrollment and a sync round-trip — reusing the same manual check as the existing
     e2e round-trip test, against the new compose stack instead of the old loose directories.
- No new automated e2e test: this is a deployment/packaging change, not a protocol change.
  `src/e2e` already covers the `brfs → bwfs → catalogsync → catalog` protocol path against
  directly-built images, independent of these compose files.
