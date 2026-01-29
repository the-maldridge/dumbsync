FROM golang:1.24-alpine AS build
RUN --mount=type=cache,target=/var/lib/apk \
    apk add tini-static

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /dumbsync-index ./cmd/index/main.go && \
    CGO_ENABLED=0 go build -o /dumbsync ./cmd/sync/main.go

FROM scratch
COPY --from=build /dumbsync-index /dumbsync-index
COPY --from=build /dumbsync /dumbsync
COPY --from=build /sbin/tini-static /tini

ENTRYPOINT ["/tini", "/dumbsync"]
