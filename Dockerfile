# syntax=docker/dockerfile:1.7
#
# The prober and its API. The status page itself is a static bundle the gateway
# serves (gateway/build-sites.ps1 in the zvm repo builds it); nothing here
# serves HTML, so a page change needs no image rebuild.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY *.go ./
# modernc.org/sqlite is pure Go, so the binary is static and the image needs no
# libc.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/zvm-status .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget
COPY --from=build /out/zvm-status /usr/local/bin/zvm-status
# Its own history is the only state, and it is the one thing here worth keeping.
VOLUME /data
ENTRYPOINT ["zvm-status"]
CMD ["-db=/data/status.db"]
