#!/bin/sh
set -e

# Demo is fully self-contained (no host bind-mounts of secrets), so unlike
# deploy/control-plane's Makefile-driven host-side password generation, the
# provisioner password is generated into the ca-data volume on first boot.
# head/base64 are confirmed present in the smallstep/step-ca Alpine base
# (openssl the CLI is not guaranteed to be).
mkdir -p /home/step/secrets
if [ ! -f /home/step/secrets/password ]; then
    head -c32 /dev/urandom | base64 > /home/step/secrets/password
fi

if [ ! -f /home/step/config/ca.json ]; then
    step ca init --deployment-type=standalone \
        --name="Miniprotector Demo CA" \
        --dns="ca,localhost" \
        --address=":9000" \
        --provisioner="admin@backup.internal" \
        --password-file=/home/step/secrets/password
fi

# Runs unconditionally, every boot (not just first init) -- an
# already-initialized CA must still pick up template changes on upgrade,
# the exact gap b212082 fixed for deploy/control-plane's own entrypoint.
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl

exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
