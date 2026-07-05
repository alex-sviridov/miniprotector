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

exec ./catalog "$STORAGE_PATH" --debug="${DEBUG:-false}"
