# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
# Cache mounts keep module/build caches out of image layers; `go build`
# fetches only the modules actually imported (not the SDK's whole graph).
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/indexer ./cmd/indexer

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 indexer
COPY --from=build /out/indexer /usr/local/bin/indexer
USER indexer
EXPOSE 8080
ENTRYPOINT ["indexer"]
CMD ["run"]
