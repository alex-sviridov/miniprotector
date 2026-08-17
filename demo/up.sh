#!/bin/sh
set -e

cd "$(dirname "$0")"
# Enables Docker Compose Bake, used by the control-plane build (see the
# Makefile's control-plane-up target). It's a no-op in THIS script: the
# per-service build loop below predates Bake and only ever hands Bake one
# target at a time, so there's nothing to fan out in parallel. The loop
# itself is deliberate, not an oversight — it was added to avoid OOM on
# memory-constrained hosts by building one Go compile at a time — so this
# export is kept for forward compatibility (a future consolidation of the
# loop into a single `docker compose build` call would pick up parallel
# builds for free) rather than being removed as dead configuration.
export COMPOSE_BAKE=true

echo "Building images..."
for svc in $(docker compose config --services); do
    echo "Building $svc..."
    docker compose build "$svc"
done

echo "Starting ca..."
docker compose up -d ca

echo "Waiting for ca to become healthy..."
timeout=30
until docker compose exec -T ca curl -fsk https://localhost:9000/health >/dev/null 2>&1; do
    timeout=$((timeout - 1))
    if [ "$timeout" -le 0 ]; then
        echo "ca did not become healthy within 30s" >&2
        exit 1
    fi
    sleep 1
done

echo "Starting issuer..."
docker compose up -d issuer

# Idempotent per-node enrollment: probes the node's own persistent volume
# for an already-redeemed bootstrap credential via a throwaway container
# (docker compose run), rather than `exec`ing into a service that may
# never have started -- correct on both a cold volume and a re-run.
# Not handled: partial volume loss where a node's bootstrap.crt is wiped but
# its clientmanager tracking record survives -- the probe would miss the
# existing DB entry, re-run `clientmanager add`, and (under set -e) abort on
# the already-tracked-hostname error. Outside this script's idempotency
# contract (which assumes a clean `down -v` + re-run), so left unhandled.
# $2, if given, is a space-separated "key=value" attribute string applied
# via `clientmanager attribute set` right after `add` and before the
# node's own container starts -- only on first enrollment (the branch
# below that runs `add` at all), so the attribute is already in
# client-manager's database before the node's first operating-refresh
# mints a certificate embedding it.
#
# The final `up -d` deliberately passes --no-deps: every service enrolled
# here depends (directly or transitively, e.g. log-gateway) only on ca/
# issuer/loki/catalog, all of which are already running by the time any
# enroll() call reaches this line, given the call order below. Without
# --no-deps, compose would also (re)create any not-yet-started dependency
# as a side effect of this command, handing it THIS service's freshly
# minted, single-use MP_CERT_TOKEN -- which it would then redeem for
# itself, permanently starving the intended node of its own token (it
# would spend the rest of its life retrying the now-already-used token).
# This is exactly what happened to database before log-gateway got its
# own enroll() call below: `enroll database` implicitly started database's
# then-unenrolled log-gateway dependency in the same `up -d`, log-gateway
# won the race to redeem database's token, and log-gateway ran ever after
# under a bootstrap credential identifying it as "database".
enroll() {
    name="$1"
    attrs="$2"
    if docker compose run --rm --no-deps --entrypoint sh "$name" \
        -c 'test -f /data/certs/bootstrap.crt' >/dev/null 2>&1; then
        echo "$name already enrolled, starting..."
        docker compose up -d --no-deps "$name"
        return
    fi
    echo "Enrolling $name..."
    token=$(docker compose exec -T ca clientmanager add "$name" \
        --ca-url https://ca:9000 \
        --root /home/step/certs/root_ca.crt \
        --password-file /home/step/secrets/password \
        --defaults-file /home/step/config/defaults.json)
    if [ -n "$attrs" ]; then
        docker compose exec -T ca clientmanager attribute set "$name" $attrs
    fi
    MP_CERT_TOKEN="$token" docker compose up -d --no-deps "$name"
}

echo "Starting loki..."
docker compose up -d loki

enroll log-gateway
enroll clientmanager-api
enroll catalog
enroll api-server
enroll policy-server
enroll database
enroll webserver "role=web"
enroll store

# Seeds a fixed, 100-file/~100-150MB dataset on database, backed up once by
# the seeded demo/policy-server/policies/backup/e2e-fixture.json policy --
# web/e2e/restore-content.spec.js restores it (with a folder rename) on
# every run rather than generating and backing up a fresh dataset per run,
# so repeated test runs don't each add ~120MB to the shared store with no
# way to reclaim it. Idempotent: skips regeneration if the fixture already
# has exactly 100 files (e.g. a re-run of this script against an
# already-seeded volume).
echo "Seeding e2e restore-content fixture data on database..."
docker compose exec -T database bash -c '
fixture=/data/e2e-restore-content-fixture
if [ "$(find "$fixture" -type f 2>/dev/null | wc -l)" = "100" ]; then
  echo "fixture already present, skipping"
  exit 0
fi
set -eu
rm -rf "$fixture"
for d in 0 1 2 3 4; do
  for s in 0 1 2 3; do
    mkdir -p "$fixture/d$d/s$s"
  done
done
i=0
while [ "$i" -lt 100 ]; do
  d=$(( i / 20 ))
  s=$(( (i / 5) % 4 ))
  size=$(shuf -i 102400-2097152 -n 1)
  path=$(printf "$fixture/d%d/s%d/file_%03d.bin" "$d" "$s" "$i")
  head -c "$size" /dev/urandom > "$path"
  i=$((i + 1))
done
'

# policy-server hot-reloads only on a write to policies/.changed
# (src/cmd/policy-server/watch.go) -- its initial ReadDir at container
# startup already picks up e2e-fixture.json on a genuinely fresh stack, but
# touching the sentinel here also covers re-running this script against an
# already-running policy-server (e.g. after a `git pull` that only added
# new policy files, with no code change to trigger a container recreate).
touch policy-server/policies/.changed

echo "Starting web..."
docker compose up -d --no-deps web

cat <<'MSG'

Demo stack is up. store's bwfs/catalogsync take up to ~30s (one reconcile tick) to start
after enrollment -- if the brfs command below fails immediately, wait a bit and retry. Try:
  docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
  docker compose -f demo/docker-compose.yml exec database ./rwfs list store:8080
  docker compose -f demo/docker-compose.yml exec database ./rwfs verify store:8080
  docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
  docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
  docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies
  docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
  docker compose -f demo/docker-compose.yml exec database ./agent list-policies
  docker compose -f demo/docker-compose.yml exec webserver ./agent list-policies
  docker compose -f demo/docker-compose.yml exec store ./agent list-policies

Reset with: docker compose -f demo/docker-compose.yml down -v
MSG
