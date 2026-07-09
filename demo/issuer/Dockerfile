FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make issuer

FROM debian:bookworm-slim
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
