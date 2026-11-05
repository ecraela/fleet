FROM golang:1.26.10-alpine3.24 AS supervisor
WORKDIR /src
COPY deploy/local/pair/supervisor/main.go ./main.go
RUN CGO_ENABLED=0 GOMAXPROCS=2 GOMEMLIMIT=256MiB go build -trimpath -ldflags='-s -w' -o /demo-node main.go

FROM alpine:3.24.2 AS dataplane
RUN apk add --no-cache curl tar
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      arm64) arch=arm64; checksum=be40adc6f2c0fd21d22219ca0fa5bf6fd11d9e5219e6408a38f4f101efdee889 ;; \
      amd64) arch=x86_64; checksum=2ff246a6ea3bab0646096e17ab94b64eda761772560c009c951fc783b8e69cff ;; \
      *) exit 1 ;; \
    esac \
    && curl --fail --silent --show-error --location "https://github.com/haproxytech/dataplaneapi/releases/download/v3.4.5/dataplaneapi_3.4.5_linux_${arch}.tar.gz" -o /tmp/dataplane.tar.gz \
    && echo "$checksum  /tmp/dataplane.tar.gz" | sha256sum -c - \
    && mkdir /out && tar -xzf /tmp/dataplane.tar.gz -C /out

FROM haproxy:3.4.7-alpine3.24
COPY --from=dataplane /out/dataplaneapi /usr/local/bin/dataplaneapi
COPY --from=supervisor /demo-node /usr/local/bin/demo-node
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/demo-node"]
