# syntax=docker/dockerfile:1
# Two images from one build:
#   docker build --target indexer -t stellar-indexer .   # fetch / write / api / run (private server)
#   docker build --target gateway -t stellar-gateway .   # public edge: UI + API proxy, no DB code
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
# Cache mounts keep module/build caches out of image layers.
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/indexer ./cmd/indexer && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM alpine:3.22 AS base
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
EXPOSE 8080

FROM base AS gateway
COPY --from=build /out/gateway /usr/local/bin/gateway
ENTRYPOINT ["gateway"]

# Last stage = default target (plain `docker build .` builds the indexer).
FROM base AS indexer
COPY --from=build /out/indexer /usr/local/bin/indexer
ENTRYPOINT ["indexer"]
CMD ["run"]
