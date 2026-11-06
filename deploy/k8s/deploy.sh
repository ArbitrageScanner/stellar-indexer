#!/usr/bin/env bash
# Deploys the private side to k3s with the namespace-scoped CI token.
# Secrets come from the environment (GitHub Actions secrets in CI) and are
# rendered as hashed kustomize Secrets, so changing any of them rolls the pods.
#
#   KUBECONFIG=… IMAGE_TAG=<git sha> POSTGRES_PASSWORD=… INTERNAL_API_KEY=… \
#   MTLS_CA_CRT_B64=… MTLS_API_CRT_B64=… MTLS_API_KEY_B64=… \
#   TUNNEL_CLIENT_KEY_B64=… TUNNEL_KNOWN_HOSTS='[edge]:2222 ssh-ed25519 …' EDGE_HOST=… \
#   deploy/k8s/deploy.sh
#
# Optional: LABELS_TOKEN, STELLAR_RPC_URL, IMAGE_PREFIX (default ghcr.io/arbitragescanner),
# GHCR_PULL_USER + GHCR_PULL_TOKEN (read:packages; for private packages), DRY_RUN=1.
set -euo pipefail

: "${IMAGE_TAG:?}" "${POSTGRES_PASSWORD:?}" "${INTERNAL_API_KEY:?}"
: "${MTLS_CA_CRT_B64:?}" "${MTLS_API_CRT_B64:?}" "${MTLS_API_KEY_B64:?}"
: "${TUNNEL_CLIENT_KEY_B64:?}" "${TUNNEL_KNOWN_HOSTS:?}" "${EDGE_HOST:?}"
NS=${NAMESPACE:-stellar-indexer}
PREFIX=${IMAGE_PREFIX:-ghcr.io/arbitragescanner}
BASE=$(cd "$(dirname "$0")" && pwd)
KUBECTL=${KUBECTL:-kubectl}
k() { "$KUBECTL" -n "$NS" "$@"; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
umask 077

{
  printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD"
  printf 'INTERNAL_API_KEY=%s\n' "$INTERNAL_API_KEY"
  [ -z "${LABELS_TOKEN:-}" ] || printf 'LABELS_TOKEN=%s\n' "$LABELS_TOKEN"
  [ -z "${STELLAR_RPC_URL:-}" ] || printf 'STELLAR_RPC_URL=%s\n' "$STELLAR_RPC_URL"
} > "$work/secrets.env"
mkdir "$work/tls" "$work/tunnel" "$work/base"
cp "$BASE"/*.yaml "$BASE"/*.env "$work/base/"
base64 -d <<<"$MTLS_CA_CRT_B64" > "$work/tls/ca.crt"
base64 -d <<<"$MTLS_API_CRT_B64" > "$work/tls/api.crt"
base64 -d <<<"$MTLS_API_KEY_B64" > "$work/tls/api.key"
base64 -d <<<"$TUNNEL_CLIENT_KEY_B64" > "$work/tunnel/id_ed25519"
printf '%s\n' "$TUNNEL_KNOWN_HOSTS" > "$work/tunnel/known_hosts"

cat > "$work/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: $NS
resources: [base]
labels:
  - pairs: { app.kubernetes.io/part-of: stellar-indexer }
images:
  - { name: ghcr.io/arbitragescanner/stellar-indexer, newName: $PREFIX/stellar-indexer, newTag: "$IMAGE_TAG" }
  - { name: ghcr.io/arbitragescanner/stellar-tunnel, newName: $PREFIX/stellar-tunnel, newTag: "$IMAGE_TAG" }
secretGenerator:
  - name: indexer-secrets
    envs: [secrets.env]
  - name: indexer-api-tls
    files: [tls/ca.crt, tls/api.crt, tls/api.key]
  - name: tunnel-client
    files: [tunnel/id_ed25519, tunnel/known_hosts]
    literals: ["EDGE_HOST=$EDGE_HOST"]
EOF
"$KUBECTL" kustomize "$work" > "$work/rendered.yaml"

if [ -n "${DRY_RUN:-}" ]; then
  k apply --dry-run=server -f "$work/rendered.yaml"
  exit 0
fi

# Private GHCR packages: a long-lived read:packages token for the node's pulls.
if [ -n "${GHCR_PULL_TOKEN:-}" ]; then
  k create secret docker-registry ghcr-pull --docker-server=ghcr.io \
    --docker-username="${GHCR_PULL_USER:-ArbitrageScanner}" --docker-password="$GHCR_PULL_TOKEN" \
    --dry-run=client -o yaml | k apply -f -
  k patch serviceaccount default -p '{"imagePullSecrets":[{"name":"ghcr-pull"}]}'
fi

applied=$(k apply -f "$work/rendered.yaml" -o name)
printf "%s\n" "$applied"
for r in statefulset/postgres statefulset/redpanda deployment/writer deployment/fetcher deployment/indexer-api deployment/tunnel; do
  k rollout status "$r" --timeout="${ROLLOUT_TIMEOUT:-600s}"
done

# Drop generated ConfigMaps/Secrets from earlier deploys (names carry a content hash).
for obj in $(k get configmap,secret -l app.kubernetes.io/part-of=stellar-indexer -o name); do
  grep -qxi "$obj" <<<"$applied" || k delete "$obj"
done
k get pods -o wide
