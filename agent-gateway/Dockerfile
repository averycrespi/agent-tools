# Build from agent-gateway/; only allowlisted source enters the context.
FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# The checked-in, generated frontend is embedded by internal/api.
RUN go build -mod=readonly -trimpath -buildvcs=false -o /out/agent-gateway ./cmd/agent-gateway

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN addgroup -g 10001 gateway \
    && adduser -D -H -u 10001 -G gateway -h /var/lib/agent-gateway gateway \
    && mkdir -p /var/lib/agent-gateway \
    && chown 10001:10001 /var/lib/agent-gateway \
    && chmod 0700 /var/lib/agent-gateway
COPY --from=build /out/agent-gateway /usr/local/bin/agent-gateway
USER 10001:10001
ENV XDG_DATA_HOME=/var/lib
WORKDIR /var/lib/agent-gateway
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
    CMD wget -q -T 2 -O /dev/null http://127.0.0.1:8210/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/agent-gateway"]
CMD ["serve"]
