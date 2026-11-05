# HTTP API: design notes

The contract is `internal/api/openapi.yaml`, served at `/openapi.yaml`. A test (`TestOpenAPICoversRoutes`) fails when a registered route is undocumented, or when the spec documents a route that doesn't exist.

## Layering

| Package | Responsibility | Must not |
|---|---|---|
| `internal/api` | Routing (one route table), middleware, handlers that parse and validate parameters → call the repository or a service → encode JSON. Services (`services.go`) orchestrate endpoints that combine several reads with domain functions (portfolio, stats, tags, leaderboards, alerts). | Contain SQL or business rules. |
| `internal/query` | The read-side repository: every SQL statement. Typed results for anything domain logic consumes (fills, prices, portfolio inputs, tag signals, tickers, candles, status). | Contain rules beyond data selection. |
| `internal/analytics` | Pure domain logic with no I/O: average-cost PnL, round-trip detection, volume breakdowns, portfolio valuation and history reconstruction, auto-tag heuristics (`Signals` → `[]Tag`, thresholds as named constants), label validation, alert merging, pair orientation. | Import `query`, `api` or the database. |

Dependencies point one way: `api → query → analytics` and `api → analytics`.

**Row sets.** Plain list endpoints (ledgers, transactions, transfers, events and so on) return the SQL projection as `[]map[string]any`. This is deliberate: the column list in the query is the contract, documented field by field in the OpenAPI schemas, and a struct per endpoint would only duplicate it. Any data that logic runs on is typed.

**Time windows.** `hours` windows end at `index_now()`, the close time of the newest indexed ledger, not at the wall clock. A paused or lagging indexer still answers "the last 24 hours" over the data it holds. `/status.lag_seconds` and `/ready` use the wall clock, because they measure lag.

**Verification.** Only verified rows feed prices, candles, PnL, leaderboards and alerts. Verified means a classic venue, or a Soroban pool/contract whose code is allowlisted for its protocol. Unverified rows are still stored and listed with `verified: false`. Protocol endpoints count verified events by default; `?verified=all` includes the rest, and `unverified_events` is always reported.

## Middleware chain (outermost first)

1. **Request ID.** Echoes a well-formed `X-Request-ID`, otherwise generates one. It appears in logs and in every error body.
2. **Observe.** Records Prometheus `http_requests_total{route,code}` and `http_request_duration_seconds{route}`. The `route` label is the registered pattern (e.g. `GET /ledgers/{seq}`), never the raw path. Also writes a debug-level access log.
3. **Panic recovery.** Returns a 500 JSON body and logs the panic.
4. **Security headers.** `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`. `/ui/` also gets a CSP that allows only its own scripts (no inline scripts). `connect-src` allows other origins so the UI can be pointed at another indexer.
5. **CORS.** Configured origins. `OPTIONS` preflights get 204.
6. **Rate limit.** Per-client token bucket. Returns 429 with `Retry-After`. `/health`, `/ready`, `/metrics` and `/ui/` are exempt.
7. **API keys.** Optional; `X-API-Key` or `Authorization: Bearer`, compared in constant time. The same paths are exempt.
8. **Router.** Heavy analytics routes go through an in-memory response cache: bounded LRU of 512 entries, keyed by URI, only 200 responses stored, concurrent identical misses share one computation, `X-Cache: HIT|MISS`.

**Errors.** `{"error", "request_id"}`. Validation fails with 400, missing entities with 404, query timeouts (20 s per statement) with 504. Anything else is 500 with a generic message; the detail is logged, never sent.

**Label writes** are closed by default. A configured `LABELS_TOKEN` must be sent as `Authorization: Bearer <token>` (otherwise 401). `LABELS_OPEN=true` allows writes without a token. With neither, writes get 403. When API keys are also enabled, send the API key in `X-API-Key`, because `Authorization` then carries the labels token.

## Configuration (`api.Config`, wired from env in `cmd`)

| Env | Config field | Meaning |
|---|---|---|
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `RateRPS` / `RateBurst` | Per-client rate; 0 = off. Burst defaults to 2×RPS+1. |
| `TRUST_PROXY` | `TrustProxy` | Take the client IP from `X-Forwarded-For` (only behind a trusted proxy). |
| `API_KEYS` | `APIKeys` | Comma-separated keys; empty = public read API. |
| `CORS_ORIGINS` | `CORSOrigins` | Comma-separated origins; default `*`. |
| `CACHE_TTL` | `CacheTTL` | Response cache TTL; 0 = off. |
| `READY_MAX_LAG` | `ReadyMaxLag` | `/ready` returns 503 when the newest ledger is older; 0 = no lag check. |
| `LABELS_TOKEN` / `LABELS_OPEN` | `LabelsToken` / `LabelsOpen` | Label write policy (see above). |
| `USD_ASSET` | `USDAsset` | Asset whose XLM market prices XLM in USD. |
| — | `Network`, `CursorName`, `UI` | Shown in `/status`; ingest cursor; embedded explorer (`/ui/`). |

## Probes

- `/health` (liveness): 200 when the process serves and Postgres answers.
- `/ready` (readiness): 200 when Postgres answers and, with `READY_MAX_LAG`, the index is fresh. Otherwise 503 with `reason` (and `lag_seconds`).
- `/status`: cursor, ledger range, pipeline component heartbeats (`components` table), gap count and the last gap (`ledger_gaps`), `rpc_tip`, `lag_ledgers`, `lag_seconds`.
