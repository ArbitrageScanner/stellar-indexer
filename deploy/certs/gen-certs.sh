#!/bin/sh
# Generates a private CA plus the certificates for mutual TLS between the
# public gateway (client) and the internal indexer API (server).
#
#   deploy/certs/gen-certs.sh [api-host-or-ip ...]    (default: 10.77.0.2 indexer-api)
#
# Copy to the private server: ca.crt api.crt api.key
# Copy to the edge server:    ca.crt gateway.crt gateway.key
# Keep ca.key offline. Certificates are valid for 825 days.
set -eu
cd "$(dirname "$0")"
mkdir -p out && cd out
[ $# -gt 0 ] || set -- 10.77.0.2 indexer-api
san=""
for h in "$@"; do
  case $h in
    *[!0-9.]*) san="${san:+$san,}DNS:$h" ;;
    *)         san="${san:+$san,}IP:$h" ;;
  esac
done
umask 077
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=stellar-indexer internal CA" -out ca.crt
openssl ecparam -name prime256v1 -genkey -noout -out api.key
openssl req -new -key api.key -subj "/CN=indexer-api" -out api.csr
printf "subjectAltName=%s\nextendedKeyUsage=serverAuth\nkeyUsage=digitalSignature\n" "$san" > api.ext
openssl x509 -req -in api.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -sha256 -extfile api.ext -out api.crt
openssl ecparam -name prime256v1 -genkey -noout -out gateway.key
openssl req -new -key gateway.key -subj "/CN=stellar-gateway" -out gateway.csr
printf "extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature\n" > gateway.ext
openssl x509 -req -in gateway.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -sha256 -extfile gateway.ext -out gateway.crt
rm -f ./*.csr ./*.ext ./*.srl
chmod 644 ca.crt api.crt gateway.crt
# The images run as uid 10001; make the keys readable by it (mounted read-only).
chmod 644 api.key gateway.key
echo "written to $(pwd): ca.crt ca.key api.crt api.key gateway.crt gateway.key (api SAN: $san)"
