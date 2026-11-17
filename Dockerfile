# syntax=docker/dockerfile:1

# Build stage: a static binary (no cgo), so it runs on a minimal base image.
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kas-core ./cmd/kas-core

# Runtime stage: distroless static has CA certificates and no shell or package
# manager. The "nonroot" variant runs as uid/gid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kas-core /kas-core
USER 65532:65532
EXPOSE 8088
ENTRYPOINT ["/kas-core"]
# The configuration file is mounted at /etc/kas/config.json. Listening on a
# non-loopback address requires -tls-cert and -tls-key (see docker-compose.yml).
CMD ["-config", "/etc/kas/config.json"]
