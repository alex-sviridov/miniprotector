#!/bin/sh
set -e

mkdir -p "$STORAGE_PATH"

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart -- no expiry check, certclient always renews when an
# identity already exists) of the long-lived bootstrap credential.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps both the bootstrap credential (daily) and the operating
# credential (every 15 min, talking to issuer) fresh continuously, so
# catalog no longer needs a container restart to pick up a renewal --
# a real improvement over the old renew-on-restart-only behavior.
./agent serve &

# Wait for agent's first operating-refresh to produce client.crt/client.key
# before starting catalog -- a fresh volume only has the bootstrap credential
# until agent's reconcile loop completes its first cycle (due immediately for
# a never-run policy); without this wait, catalog would race agent and could
# crash-loop on a genuinely fresh deployment's first boot.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

exec ./catalog "$STORAGE_PATH" --debug="${DEBUG:-false}"
