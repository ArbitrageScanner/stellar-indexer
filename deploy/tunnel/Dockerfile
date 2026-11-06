# Outbound-only link from the hidden indexer (k3s) to the public edge.
#   client (k3s pod):  ssh -R 0.0.0.0:18080:$TARGET  → edge
#   server (edge):     sshd on :2222, one user, remote forwards to :18080 only
# The forwarded port exists only inside the edge's compose network.
FROM alpine:3.22
RUN apk add --no-cache openssh-client openssh-server-common openssh-server tini && \
    adduser -D -u 10001 -s /sbin/nologin tunnel && echo 'tunnel:*' | chpasswd -e && \
    rm -f /etc/ssh/ssh_host_*
COPY entrypoint.sh /usr/local/bin/tunnel
ENTRYPOINT ["/sbin/tini", "--", "tunnel"]
