FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /build/bin/clientmanager ./cmd/clientmanager

FROM smallstep/step-ca

USER root
COPY --chmod=0755 --from=builder /build/bin/clientmanager /usr/local/bin/clientmanager
RUN mkdir -p /home/step/templates /data/client-manager && chown step:step /data/client-manager
COPY --chmod=0644 deploy/control-plane/ca/templates/leaf.tpl /home/step/templates/leaf.tpl
COPY --chmod=0755 demo/ca/entrypoint.sh /home/step/entrypoint.sh
USER step

ENTRYPOINT ["/home/step/entrypoint.sh"]
