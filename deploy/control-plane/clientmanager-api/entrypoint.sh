#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart) of the long-lived bootstrap credential.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps both the bootstrap credential (daily) and the operating
# credential (every 15 min, talking to issuer) fresh continuously.
./agent serve &

# Wait for agent's first operating-refresh to produce client.crt/client.key
# before starting clientmanager-api.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

# listener is this host's only control-plane gRPC port (:9300); the service
# below listens on an internal socket. Restart-looped so a crash cannot strand
# the host (the service itself is exec'd/backgrounded, so nothing else would
# notice).
( while true; do ./listener --debug="${DEBUG:-false}" || true; sleep 1; done ) &

./clientmanager-api --debug="${DEBUG:-false}" &
./clientmanager-admin-api --debug="${DEBUG:-false}" \
	--ca-url https://step-ca:9000 \
	--root /data/root_ca.crt \
	--provisioner admin@backup.internal \
	--password-file /data/secrets/password &
wait
