#!/bin/sh
# tunnel client | server   (configuration through environment, keys as files)
set -eu
mode=${1:-client}
key_dir=${KEY_DIR:-/etc/tunnel}
listen_port=${LISTEN_PORT:-18080}

case $mode in
client)
  : "${EDGE_HOST:?}" "${TARGET:?host:port to expose, e.g. indexer-api:8080}"
  # Secret mounts are group-readable; ssh insists on a private key file.
  d=$(mktemp -d); cp "$key_dir/id_ed25519" "$d/id"; chmod 600 "$d/id"
  while true; do
    ssh -N -T -p "${EDGE_PORT:-2222}" -i "$d/id" \
      -o IdentitiesOnly=yes -o BatchMode=yes \
      -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$key_dir/known_hosts" \
      -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes \
      -o ConnectTimeout=15 -R "0.0.0.0:$listen_port:$TARGET" "tunnel@$EDGE_HOST" || true
    echo "tunnel: disconnected, retrying in 5s" >&2
    sleep 5
  done
  ;;
server)
  install -m 600 "$key_dir/ssh_host_ed25519_key" /etc/ssh/ssh_host_ed25519_key
  install -m 644 "$key_dir/authorized_keys" /etc/ssh/tunnel_authorized_keys
  cat > /etc/ssh/sshd_config <<CFG
Port 2222
HostKey /etc/ssh/ssh_host_ed25519_key
AuthorizedKeysFile /etc/ssh/tunnel_authorized_keys
AllowUsers tunnel
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowTcpForwarding remote
GatewayPorts clientspecified
PermitListen 0.0.0.0:$listen_port
PermitOpen none
AllowAgentForwarding no
AllowStreamLocalForwarding no
X11Forwarding no
PermitTTY no
PermitTunnel no
ForceCommand /bin/false
ClientAliveInterval 15
ClientAliveCountMax 3
MaxAuthTries 2
MaxStartups 10:30:60
LoginGraceTime 15
LogLevel VERBOSE
CFG
  exec /usr/sbin/sshd -D -e
  ;;
*) echo "usage: tunnel client|server" >&2; exit 2 ;;
esac
