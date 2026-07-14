# deploy/control-plane/clientmanager-api/Dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make clientmanager-api certclient agent policyclient

FROM timberio/vector:0.46.0-debian AS vector-source

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/clientmanager-api /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/clientmanager-api/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
