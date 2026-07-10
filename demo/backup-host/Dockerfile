FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalogsync certclient agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalogsync /build/bin/certclient /build/bin/agent ./
COPY --chmod=0755 demo/backup-host/entrypoint.sh ./entrypoint.sh
ENTRYPOINT ["./entrypoint.sh"]
