FROM golang:1.26.11-alpine3.24 AS build
WORKDIR /src
COPY deploy/local/backend.go ./backend.go
RUN CGO_ENABLED=0 GOMAXPROCS=2 GOMEMLIMIT=256MiB go build -trimpath -ldflags='-s -w' -o /demo-backend backend.go

FROM alpine:3.24.2
COPY --from=build /demo-backend /usr/local/bin/demo-backend
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/demo-backend"]
