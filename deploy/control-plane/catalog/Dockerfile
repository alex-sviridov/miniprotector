FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make catalog certclient agent policyclient

FROM timberio/vector:0.46.0-debian AS vector-source

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates sqlite3 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/catalog/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
