FROM --platform=$BUILDPLATFORM node:24.19.0-alpine3.24 AS console
RUN apk add --no-cache python3
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@11.25.0 --activate
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY . /src/
RUN NODE_OPTIONS=--max-old-space-size=384 pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine3.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=console /src/internal/webui/dist/ ./internal/webui/dist/
ARG VERSION=dev
ARG REVISION=unknown
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    export GOMAXPROCS=2 GOMEMLIMIT=192MiB GOFLAGS=-p=1 GOOS=$TARGETOS GOARCH=$TARGETARCH; \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X github.com/ecraela/fleet/internal/buildinfo.Version=$VERSION -X github.com/ecraela/fleet/internal/buildinfo.Revision=$REVISION" -o /out/haproxy-manager ./cmd/haproxy-manager \
    && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X github.com/ecraela/fleet/internal/buildinfo.Version=$VERSION -X github.com/ecraela/fleet/internal/buildinfo.Revision=$REVISION" -o /out/haproxy-agent ./cmd/haproxy-agent \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/demo-backend ./deploy/local/backend.go

FROM alpine:3.24.2 AS runtime-base
RUN apk add --no-cache ca-certificates curl \
    && addgroup -S -g 10001 haproxy-manager \
    && adduser -S -D -H -u 10001 -G haproxy-manager haproxy-manager

FROM runtime-base AS container-sources
COPY scripts/legal.py scripts/container_sources.py /collector/
RUN --mount=type=cache,id=haproxy-aports,target=/cache/aports,sharing=locked \
    cp /lib/apk/db/installed /tmp/runtime-installed \
    && apk add --no-cache python3 git abuild curl xz \
    && APORTS_CACHE=/cache/aports DISTFILES_MIRROR=https://distfiles.alpinelinux.org/distfiles/v3.24 \
       python3 /collector/container_sources.py --database /tmp/runtime-installed \
       --output /out/legal --work /tmp/source-collection

FROM runtime-base AS runtime
COPY --from=build /out/ /usr/local/bin/
COPY --from=console /src/internal/webui/dist/legal/ /usr/share/haproxy-manager/
COPY --from=container-sources /out/legal/ /usr/share/haproxy-manager/
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/haproxy-manager"]
