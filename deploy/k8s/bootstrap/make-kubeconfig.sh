#!/usr/bin/env sh
# Prints a kubeconfig for the namespace-scoped CI deployer. Run where kubectl
# has cluster-admin (e.g. on the k3s node). Usage: make-kubeconfig.sh [api-url]
set -eu
NS=stellar-indexer
SERVER=${1:-https://203.0.113.20:6443}
TOKEN=$(kubectl -n "$NS" get secret ci-deployer-token -o jsonpath='{.data.token}' | base64 -d)
CA=$(kubectl -n "$NS" get secret ci-deployer-token -o jsonpath='{.data.ca\.crt}')
cat <<KCFG
apiVersion: v1
kind: Config
clusters:
  - name: k3s
    cluster: { server: $SERVER, certificate-authority-data: $CA }
users:
  - name: ci-deployer
    user: { token: $TOKEN }
contexts:
  - name: $NS
    context: { cluster: k3s, user: ci-deployer, namespace: $NS }
current-context: $NS
KCFG
