#!/bin/sh
set -e
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
