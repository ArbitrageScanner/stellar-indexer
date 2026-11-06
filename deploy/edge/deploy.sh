#!/usr/bin/env bash
# Deploys the public edge (Caddy + gateway + tunnel endpoint) over SSH to a
# Docker host. Configuration and secrets come from the environment (GitHub
# Actions in CI); nothing is built on the host, images are pulled.
#
#   EDGE_SSH=deploy@edge.example.org IMAGE_TAG=<git sha> DOMAIN=stellar.example.org \
#   INTERNAL_API_KEY=… MTLS_CA_CRT_B64=… MTLS_GATEWAY_CRT_B64=… MTLS_GATEWAY_KEY_B64=… \
#   TUNNEL_HOST_KEY_B64=… TUNNEL_CLIENT_PUB='ssh-ed25519 AAAA…' \
#   deploy/edge/deploy.sh
#
# Optional: IMAGE_PREFIX (default ghcr.io/arbitragescanner), REGISTRY_USER +
# REGISTRY_TOKEN (used for this pull only, not stored on the host),
# PUBLIC_API_KEYS, CORS_ORIGINS, RATE_LIMIT_RPS, RATE_LIMIT_BURST, EDGE_DIR,
# PULL=0 (images already on the host), SSH_OPTS (e.g. "-i key -o UserKnownHostsFile=known_hosts").
set -euo pipefail

: "${EDGE_SSH:?}" "${IMAGE_TAG:?}" "${DOMAIN:?}" "${INTERNAL_API_KEY:?}"
: "${MTLS_CA_CRT_B64:?}" "${MTLS_GATEWAY_CRT_B64:?}" "${MTLS_GATEWAY_KEY_B64:?}"
: "${TUNNEL_HOST_KEY_B64:?}" "${TUNNEL_CLIENT_PUB:?}"
PREFIX=${IMAGE_PREFIX:-ghcr.io/arbitragescanner}
DIR=${EDGE_DIR:-/opt/app/stellar-edge}
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck disable=SC2086
remote() { ssh ${SSH_OPTS:-} -o BatchMode=yes "$EDGE_SSH" "$@"; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
umask 077
mkdir -p "$work/certs" "$work/tunnel"
cp "$HERE/docker-compose.yml" "$HERE/compose.tunnel.yml" "$HERE/Caddyfile" "$work/"
cat > "$work/.env" <<EOF
COMPOSE_PROJECT_NAME=stellar-edge
COMPOSE_FILE=docker-compose.yml:compose.tunnel.yml
GATEWAY_IMAGE=$PREFIX/stellar-gateway:$IMAGE_TAG
TUNNEL_IMAGE=$PREFIX/stellar-tunnel:$IMAGE_TAG
DOMAIN=$DOMAIN
INTERNAL_API_KEY=$INTERNAL_API_KEY
CERTS_DIR=./certs
PUBLIC_API_KEYS=${PUBLIC_API_KEYS:-}
CORS_ORIGINS=${CORS_ORIGINS:-*}
RATE_LIMIT_RPS=${RATE_LIMIT_RPS:-20}
RATE_LIMIT_BURST=${RATE_LIMIT_BURST:-40}
EOF
base64 -d <<<"$MTLS_CA_CRT_B64" > "$work/certs/ca.crt"
base64 -d <<<"$MTLS_GATEWAY_CRT_B64" > "$work/certs/gateway.crt"
base64 -d <<<"$MTLS_GATEWAY_KEY_B64" > "$work/certs/gateway.key"
base64 -d <<<"$TUNNEL_HOST_KEY_B64" > "$work/tunnel/ssh_host_ed25519_key"
printf 'restrict,port-forwarding,permitlisten="0.0.0.0:18080" %s\n' "$TUNNEL_CLIENT_PUB" > "$work/tunnel/authorized_keys"
# The gateway runs as uid 10001: its certs dir must be traversable/readable
# inside the container; the deploy dir itself (0750) keeps other host users out.
chmod 755 "$work/certs"; chmod 644 "$work/certs/"*
chmod 600 "$work/.env"

tar -C "$work" -czf - . | remote "install -d -m 750 '$DIR' && tar -xzf - -C '$DIR' --no-same-owner && chmod 750 '$DIR'"

# Registry credentials travel on stdin and live in a temp DOCKER_CONFIG for
# this pull only; running containers never need them again.
printf '%s\n%s\n' "${REGISTRY_USER:-}" "${REGISTRY_TOKEN:-}" | remote "set -e; cd '$DIR'
  read -r user; read -r token
  export DOCKER_CONFIG=\$(mktemp -d); trap 'rm -rf \"\$DOCKER_CONFIG\"' EXIT
  [ -z \"\$token\" ] || printf '%s' \"\$token\" | docker login ghcr.io -u \"\$user\" --password-stdin >/dev/null
  [ '${PULL:-1}' = 0 ] || docker compose pull --quiet
  docker compose up -d --no-build --remove-orphans --wait --wait-timeout 180
  docker compose ps --format 'table {{.Service}}\t{{.Image}}\t{{.Status}}'
  docker image prune -f >/dev/null"
