# Local testing only. The Go module checksum database verifies this pinned release.
FROM golang:1.26.10-alpine3.24 AS build
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOBIN=/out CGO_ENABLED=0 GOMAXPROCS=2 GOMEMLIMIT=256MiB \
    go install github.com/letsencrypt/pebble/v2/cmd/pebble@v2.10.1
FROM alpine:3.24.2
COPY --from=build /out/pebble /usr/local/bin/pebble
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/pebble"]
