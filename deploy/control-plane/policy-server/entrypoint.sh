#!/bin/sh
set -e

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
# policy-server never needs a container restart to pick up a renewal.
./agent serve &

# Wait for agent's first operating-refresh to produce client.crt/client.key
# before starting policy-server -- a fresh volume only has the bootstrap
# credential until agent's reconcile loop completes its first cycle (due
# immediately for a never-run policy); without this wait, policy-server
# would race agent and could crash-loop on a genuinely fresh deployment's
# first boot.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

# No mkdir/STORAGE_PATH step here, unlike catalog's entrypoint -- policy-server
# has no local database; its own main.go already os.MkdirAll's
# $MP_CONFIG_PATH/policies on startup.
exec ./policy-server --debug="${DEBUG:-false}"
