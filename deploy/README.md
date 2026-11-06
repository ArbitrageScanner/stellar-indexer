# Split deployment: public edge + hidden indexer

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
