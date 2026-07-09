#!/bin/sh
set -e

cd "$(dirname "$0")"

echo "Building images..."
docker compose build

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
enroll() {
    name="$1"
    if docker compose run --rm --no-deps --entrypoint sh "$name" \
        -c 'test -f /data/certs/bootstrap.crt' >/dev/null 2>&1; then
        echo "$name already enrolled, starting..."
        docker compose up -d "$name"
        return
    fi
    echo "Enrolling $name..."
    token=$(docker compose exec -T ca clientmanager add "$name" \
        --ca-url https://ca:9000 \
        --root /home/step/certs/root_ca.crt \
        --password-file /home/step/secrets/password \
        --defaults-file /home/step/config/defaults.json)
    MP_CERT_TOKEN="$token" docker compose up -d "$name"
}

enroll catalog
enroll client
enroll store

cat <<'MSG'

Demo stack is up. Try:
  docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample-data --destination store:8080
  docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
  docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
  docker compose -f demo/docker-compose.yml logs -f store
  docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
  docker compose -f demo/docker-compose.yml exec store ./agent list-policies

Reset with: docker compose -f demo/docker-compose.yml down -v
MSG
