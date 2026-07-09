#!/bin/sh
set -e

if [ -n "$STORAGE_PATH" ]; then
    mkdir -p "$STORAGE_PATH"
fi

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart) of the long-lived bootstrap credential -- same
# pattern as deploy/control-plane/catalog/entrypoint.sh.
if [ -f /data/certs/bootstrap.crt ]; then
    ./certclient renew
else
    ./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

./agent serve &
AGENT_PID=$!

# Wait for agent's first operating-refresh to produce client.crt/client.key
# (due immediately for a never-run policy -- see cmd/agent/reconcile.go's
# isDue) before starting any workload that needs it.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
    sleep 1
    timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
    echo "agent did not produce an operating certificate within 60s" >&2
    exit 1
fi

if [ -n "$STORAGE_PATH" ]; then
    ./bwfs "$STORAGE_PATH" server --port 8080 --debug="${DEBUG:-false}" &
    BWFS_PID=$!
    ./catalogsync "$STORAGE_PATH" --debug="${DEBUG:-false}" &
    CATALOGSYNC_PID=$!
fi

# Set only now (not before backgrounding) so the shell process -- which
# never execs away, unlike catalog's entrypoint -- stays this container's
# PID 1 and keeps receiving TERM directly, with a trap that's still live
# to forward it to every backgrounded child.
trap 'kill $AGENT_PID $BWFS_PID $CATALOGSYNC_PID 2>/dev/null || true' TERM
wait
