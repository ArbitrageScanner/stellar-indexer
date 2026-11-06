#!/usr/bin/env bash
# Generates every deployment secret once, as KEY=VALUE lines (multi-line
# values base64-encoded) for GitHub Actions environment secrets/variables.
#
#   deploy/secrets/gen-secrets.sh <edge-host> <out.env>      # e.g. edge.example.org
#
# Also writes <out.env>.ca.key (the mTLS CA key): keep it offline, it is only
# needed to issue new certificates. Public values (*_PUB, TUNNEL_KNOWN_HOSTS)
# can be plain variables. Nothing is printed to the terminal.
set -euo pipefail
EDGE=${1:?edge host}
OUT=${2:?output file}
[ ! -e "$OUT" ] || { echo "$OUT exists; refusing to overwrite secrets" >&2; exit 1; }
umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cp "$(dirname "$0")/../certs/gen-certs.sh" "$work/"
sh "$work/gen-certs.sh" indexer-api >/dev/null
c=$work/out
ssh-keygen -q -t ed25519 -N '' -C stellar-indexer-tunnel -f "$work/tunnel"
ssh-keygen -q -t ed25519 -N '' -C stellar-edge-tunnel-host -f "$work/host"
ssh-keygen -q -t ed25519 -N '' -C github-actions-deploy -f "$work/deploy"

b64() { base64 -w0 < "$1"; }
{
  echo "# secrets (environment: production)"
  echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)"
  echo "INTERNAL_API_KEY=$(openssl rand -hex 32)"
  echo "LABELS_TOKEN=$(openssl rand -hex 32)"
  echo "MTLS_CA_CRT_B64=$(b64 "$c/ca.crt")"
  echo "MTLS_API_CRT_B64=$(b64 "$c/api.crt")"
  echo "MTLS_API_KEY_B64=$(b64 "$c/api.key")"
  echo "MTLS_GATEWAY_CRT_B64=$(b64 "$c/gateway.crt")"
  echo "MTLS_GATEWAY_KEY_B64=$(b64 "$c/gateway.key")"
  echo "TUNNEL_CLIENT_KEY_B64=$(b64 "$work/tunnel")"
  echo "TUNNEL_HOST_KEY_B64=$(b64 "$work/host")"
  echo "DEPLOY_SSH_KEY_B64=$(b64 "$work/deploy")"
  echo "# variables (public)"
  echo "TUNNEL_CLIENT_PUB=$(cat "$work/tunnel.pub")"
  echo "TUNNEL_KNOWN_HOSTS=[$EDGE]:2222 $(cut -d' ' -f1,2 "$work/host.pub")"
  echo "DEPLOY_SSH_PUB=$(cat "$work/deploy.pub")"
} > "$OUT"
cp "$c/ca.key" "$OUT.ca.key"
echo "wrote $OUT and $OUT.ca.key (mode 600)"
