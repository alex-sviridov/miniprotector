FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make issuer

FROM debian:bookworm-slim
# uid/gid 1000 below must match the smallstep/step-ca base image's `step` user
# (see demo/ca/Dockerfile), so the shared client-manager-data volume stays
# writable by both clientmanager (running as `step` in ca's container) and
# issuer here. If that base image's step uid ever changes, this coupling
# breaks silently with an ownership mismatch.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 issuer && useradd -u 1000 -g 1000 -M -s /usr/sbin/nologin issuer \
    && mkdir -p /data && chown issuer:issuer /data

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
USER issuer
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://ca:9000", \
            "--root", "/ca-data/certs/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/ca-data/secrets/password"]
