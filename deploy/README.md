# Split deployment: public edge + hidden indexer

> **Production layout** (k3s private side + Docker edge, deployed by CI) is described in
> ["Production: k3s + Docker edge"](#production-k3s--docker-edge) below. The sections before it
> describe the generic two-Docker-host variant over WireGuard.

```
 Internet ──HTTPS──▶ EDGE server (small, public)               PRIVATE server (large, hidden)
                     ┌──────────────────────────┐   WireGuard   ┌─────────────────────────────────┐
                     │ Caddy (TLS, HSTS)        │◀══ tunnel ════│ api (internal: key + mTLS)      │
                     │  └▶ gateway              │  initiated by │ writer ◀ redpanda ◀ fetcher ◀── RPC
                     │      UI + API proxy      │  the private  │   └────▶ postgres               │
                     │      rate limit, cache   │  side (no     │                                 │
                     │      no DB code/secrets  │  inbound port)│ firewall: drop all inbound      │
                     └──────────────────────────┘               └─────────────────────────────────┘
```

| | Edge (public) | Private (indexer) |
|---|---|---|
| Runs | `caddy`, `gateway` (`stellar-gateway` image: no DB, Kafka or Stellar code) | `postgres`, `redpanda`, `fetcher`, `writer`, `api` |
| Inbound from internet | TCP 80/443, UDP 51820 (WireGuard) | **none** |
| Outbound | — | HTTPS to Stellar RPC, UDP to the edge's WireGuard |
| Secrets held | internal API key, gateway client certificate | DB password, internal API key, API server certificate, labels token |

The private server dials the tunnel itself (`PersistentKeepalive`), so it can sit behind NAT with every inbound port closed. Even if the edge is compromised, the attacker gets only what the public already sees: the read API. There are no database credentials, and the internal API is read-only apart from label writes, which need `LABELS_TOKEN`.

Defense in depth on the internal API, each layer verified (see "Verification" below):
1. It is reachable only through the tunnel (bound to the WireGuard IP; nftables drops everything else).
2. mTLS: only a client certificate signed by your private CA completes the handshake.
3. An API key (`INTERNAL_API_KEY`) is required on every request.

## Setup

### 1. Tunnel (WireGuard)
On both servers, `apt install wireguard` and generate keys with `wg genkey | tee private.key | wg pubkey > public.key`.
- Edge: [`wireguard/edge-wg0.conf.example`](wireguard/edge-wg0.conf.example) → `/etc/wireguard/wg0.conf`; open UDP 51820.
- Private: [`wireguard/private-wg0.conf.example`](wireguard/private-wg0.conf.example) → `/etc/wireguard/wg0.conf` (`Endpoint` = the edge).
- Run `systemctl enable --now wg-quick@wg0` on both, then check from the edge: `ping 10.77.0.2`.
- Private firewall: [`wireguard/private-nftables.conf.example`](wireguard/private-nftables.conf.example). Allow SSH only over `wg0`, and use the provider console to recover.
- Start WireGuard before Docker (`systemctl edit docker` → `After=wg-quick@wg0.service`), because the API binds to the tunnel IP.

> **Docker bypasses ufw/firewalld.** Published ports are inserted into iptables ahead of host firewall rules. This is why the private compose binds the API to `PRIVATE_BIND` (the WireGuard IP) and publishes nothing else. Never bind to `0.0.0.0` there.

### 2. Certificates (mTLS)
```bash
deploy/certs/gen-certs.sh 10.77.0.2          # SAN = the private server's tunnel IP
# private server: ca.crt api.crt api.key      edge server: ca.crt gateway.crt gateway.key
# keep ca.key offline
```

### 3. Private server
```bash
cd deploy/private && cp .env.example .env     # set POSTGRES_PASSWORD, INTERNAL_API_KEY (openssl rand -hex 32),
                                              # PRIVATE_BIND=10.77.0.2, TLS_* paths, retention
docker compose up -d --build --wait
```

### 4. Edge server
```bash
cd deploy/edge && cp .env.example .env        # DOMAIN, the same INTERNAL_API_KEY,
                                              # UPSTREAM_URL=https://10.77.0.2:8080, UPSTREAM_* cert paths
docker compose up -d --build --wait
curl https://$DOMAIN/ready                    # 200 when the indexer behind the tunnel is fresh
```
The edge is stateless; run several of them behind DNS or a load balancer if needed.

### Gateway settings
| Var | Default | |
|---|---|---|
| `UPSTREAM_URL` | — | internal API (`https://10.77.0.2:8080` with mTLS) |
| `UPSTREAM_API_KEY` | — | the private API's `API_KEYS` value (required for plain http) |
| `UPSTREAM_CA_FILE`, `UPSTREAM_CERT_FILE`, `UPSTREAM_KEY_FILE`, `UPSTREAM_SERVER_NAME` | — | upstream TLS / client certificate |
| `TRUST_PROXY` | `false` | `true` behind Caddy (it sets `X-Forwarded-For`); client-sent forwarding headers are otherwise replaced |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `20` / `40` | per client IP |
| `EDGE_CACHE_TTL` | `5s` | GET responses (not `/status`); `X-Cache: HIT\|MISS` |
| `API_KEYS` | — | optional public keys required from clients (swapped for the internal key upstream) |
| `BLOCK_PATHS` | `/metrics,/stats/tables` | never proxied |
| `CORS_ORIGINS` | `*` | |
| `METRICS_ADDR` | `127.0.0.1:9100` | the gateway's own metrics, on a separate listener |
| `UI_ENABLED` / `UI_DIR` | `true` / — | serve the explorer |

`/health` is the gateway's liveness; `/ready` checks the private API through the tunnel. When the indexer is unreachable, API calls return 502 `{"error":"indexer API unavailable"}`. If the gateway's own credentials are rejected, clients also get a 502, and the details go only to the gateway log and to `gateway_upstream_errors_total{kind="auth"}`.

### Alternative tunnel: SSH reverse forward
If WireGuard is not an option, the private server can hold a reverse SSH tunnel instead. It is outbound-only as well:
```bash
# private server: PRIVATE_BIND=127.0.0.1, certificates from `gen-certs.sh indexer-api`,
# and a systemd service (Restart=always) running:
autossh -M 0 -N -o ServerAliveInterval=15 -o ExitOnForwardFailure=yes \
  -R 127.0.0.1:18080:127.0.0.1:8080 tunnel@edge.example.org
# edge: a dedicated user whose authorized_keys line has
#   restrict,port-forwarding,permitlisten="127.0.0.1:18080",command="/bin/false"
# gateway: UPSTREAM_URL=https://host.docker.internal:18080, UPSTREAM_SERVER_NAME=indexer-api,
#          extra_hosts: ["host.docker.internal:host-gateway"]
```

### Static UI elsewhere (CDN)
The explorer is static files (`web/static`). To host them separately, set `apiBase` in `web/static/config.js` to the gateway's public URL.

## Verification (performed locally)

Two Docker projects simulate the servers. The private project publishes no ports, and the projects share only an `internal: true` network that stands in for the tunnel. Results:

| Check | Result |
|---|---|
| Public HTTPS → Caddy → gateway → mTLS → internal API: `/status`, `/ready`, `/ui/` (CSP, HSTS) | 200 |
| `/metrics`, `/stats/tables` via the edge | 404 (blocked) |
| Edge cache on `/pairs` | MISS → HIT |
| Internal API without client certificate | TLS handshake refused |
| Internal API with a client certificate from another CA | TLS handshake refused |
| Internal API with a valid certificate but no API key | 401 |
| Internal API from a network without the tunnel | unreachable |
| Label write via edge: no token / `LABELS_TOKEN` | 401 / 200 |
| 120 rapid requests from one client | 87 × 200, 33 × 429 |
| Private API stopped | edge `/ready` 503, API 502 (no internals); recovers automatically |
| Browser smoke test of every explorer page through the gateway | all page views ok |

Unit tests (`internal/gateway`) cover the same behaviour, including real mTLS handshakes with generated certificates. WireGuard and nftables themselves were not exercised here, because the dev container has no kernel networking privileges. Their configs are standard and listed above.

## Production: k3s + Docker edge

```
 Internet ─HTTPS─▶ edge 203.0.113.10 (Docker)                       k3s node 203.0.113.20, namespace stellar-indexer
                   caddy ─▶ gateway ─mTLS─▶ tunnel:18080 ◀═ SSH -R ═ tunnel pod ─▶ indexer-api ─▶ postgres
                                            (sshd :2222, only            (outbound only)   writer ◀ redpanda ◀ fetcher ◀ RPC
                                             from 203.0.113.20)
```

The private side runs in its own namespace on a k3s node shared with other workloads, so the design avoids touching the host:
- Nothing in the namespace is exposed: Services are ClusterIP only, the namespace quota forbids NodePort and LoadBalancer, and a NetworkPolicy admits only pods of the same namespace (checked: a pod in `default` cannot reach `indexer-api` or `postgres`).
- The link to the edge is a pod (`deploy/k8s/tunnel.yaml`, image `stellar-tunnel`) that dials the edge's sshd container and remote-forwards `indexer-api:8080`. The forwarded port exists only on the edge's compose network. On top of it, the gateway still uses mTLS (the server name is `indexer-api`) and the internal API key.
- On the edge, the sshd accepts one key, for remote forwards to `:18080` only (no shell, TTY or local forwards). Port 2222 is allowed only from the k3s node through the `DOCKER-USER` chain (`deploy/docker-host.sh`, `TUNNEL_ALLOW_FROM`), because ufw does not filter Docker-published ports.
- The namespace runs under Pod Security `restricted`, so every container is non-root with all capabilities dropped. A `ResourceQuota` caps it at 16 CPU / 48 Gi requested, 32 CPU / 64 Gi limits and 800 Gi of storage.

| Piece | Files |
|---|---|
| Namespace, quota, PSS, CI identity (once, by a cluster admin) | `k8s/bootstrap/namespace.yaml`, `k8s/bootstrap/make-kubeconfig.sh` |
| Workloads (kustomize) | `k8s/*.yaml`, settings in `k8s/indexer.env` |
| k3s deploy (secrets become hashed Secrets, so changes roll the pods) | `k8s/deploy.sh` |
| Edge deploy over SSH (pulls images, keeps no registry credentials) | `edge/deploy.sh`, `edge/compose.tunnel.yml` |
| Docker host preparation (once, as root) | `docker-host.sh` |
| All secrets and keys, generated once | `secrets/gen-secrets.sh <edge-host> <out.env>` |

The CI identity is the ServiceAccount `stellar-indexer/ci-deployer`, bound to the built-in `edit` ClusterRole by a RoleBinding. It can manage workloads, Services, Secrets, PVCs and NetworkPolicies in this namespace only. It cannot read other namespaces, nodes or cluster-scoped objects, and it cannot change RBAC or the quota (checked with `kubectl auth can-i`).

### CI/CD
`.github/workflows/ci.yml` runs lint, tests, the Docker build and the testnet e2e. On `main`, it then publishes `ghcr.io/<owner>/stellar-{indexer,gateway,tunnel}:<sha>` and, when the variable `DEPLOY_ENABLED=true` is set, calls `deploy.yml`. That workflow deploys k3s first (migrations run there), then the edge, then smoke-tests `https://<EDGE_DOMAIN>/ready` and `/ui/`. To roll back, run `deploy.yml` by hand with an older sha. The secrets and variables (GitHub environment `production`) are listed at the top of `deploy.yml`.

All three indexer processes run the same tag. Old binaries refuse a database at a newer migration, so never pin them to different shas.

### First-time setup
1. k3s: `kubectl apply -f deploy/k8s/bootstrap/namespace.yaml`, then `make-kubeconfig.sh` → secret `KUBECONFIG_B64`.
2. Edge: `TUNNEL_ALLOW_FROM=203.0.113.20 deploy/docker-host.sh`, then add the CI key to `deploy`.
3. `deploy/secrets/gen-secrets.sh 203.0.113.10 out.env` → GitHub secrets and variables. Keep `out.env.ca.key` offline.
4. Point DNS at the edge and set `EDGE_DOMAIN`. Caddy accepts a list, e.g. `explorer.example.org, 203-0-113-10.sslip.io`.
