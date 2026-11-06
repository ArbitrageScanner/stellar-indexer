# HTTP API

The contract is `internal/api/openapi.yaml`, served at `/openapi.yaml`. A test (`TestOpenAPICoversRoutes`) fails when a registered route is undocumented, or when the spec documents a route that doesn't exist.

## Endpoints

Middleware, security and probes are described below. The read API is public by default; set `API_KEYS` to require a key. Label writes are closed unless `LABELS_TOKEN` or `LABELS_OPEN` is set. Requests are rate-limited per client IP, and heavy analytics responses are cached for `CACHE_TTL`. Analytics windows end at the newest indexed ledger (`index_now()`). Prices, candles, PnL, leaderboards and alerts use verified fills only.

Asset parameters accept `native`/`XLM`, `CODE:ISSUER`, or a contract id.

| Endpoint | Description |
|---|---|
| `/health`, `/ready`, `/metrics`, `/openapi.yaml` | liveness, readiness (DB + freshness ≤ `READY_MAX_LAG`), Prometheus metrics, API contract |
| `/status` | cursor, lag, pipeline component heartbeats, ledger gaps |
| `/search?q` | classify a ledger number, tx hash, G…/C… address or asset code into entities in the index |
| `/ledgers?limit&before`, `/ledgers/{seq}` | ledgers with tx list |
| `/transactions?successful&soroban&source&contract&function&ledger&before` | filtered tx feed (page with `before=<id>`) |
| `/transactions/{hash}` | tx with operations, transfers, trades, contract and protocol events |
| `/operations?type&source&successful&before` | typed operation feed |
| `/accounts/top`, `/accounts/{id}` | balances, trustlines, offers, 24h activity |
| `/accounts/{id}/operations?before`, `/transfers`, `/trades`, `/events` | account history (`events` = protocol events as user) |
| `/assets?hours`, `/assets/{key}`, `/assets/{key}/holders` | most active assets; per-asset transfer, mint and burn totals and top pairs; largest indexed holders |
| `/pairs?hours` | top pairs: trades, volume, VWAP, last price, venues |
| `/trades?base&counter&venue&taker&pool&before` | latest fills |
| `/transfers?asset&type&before` | token-flow feed |
| `/price?base&counter` | last price, 24h open/high/low/volume/change |
| `/candles?base&counter&interval=1m\|5m\|15m\|30m\|1h\|4h\|1d&from&to` | OHLCV |
| `/pools` | classic AMM pools: reserves and spot price |
| `/logos`, `/assets/{key}/logo` | asset keys with a resolved issuer logo; the logo itself (PNG, re-encoded by the indexer) |
| `/protocols?hours` | per-protocol events, users, contracts, action breakdown; trades per venue |
| `/protocols/{name}/summary?hours` | actions, per-asset amounts, contracts, users, pools and hourly timeline for one protocol |
| `/protocols/{name}/events?action&user&contract&asset&before` | decoded protocol events |
| `/contracts?hours`, `/contracts/{id}` | most active contracts (calls + events); one contract's functions, topics and protocol actions |
| `/contracts/{id}/events?topic&before`, `/events?contract&topic&protocol&before` | raw decoded contract events (`protocol=none` = no decoder matched) |
| `/stats/network?hours&bucket=minute\|hour\|day` | tx, ops, failure, Soroban share and fees over time |
| `/stats/operations?hours` | operation mix |
| `/stats/tables` | table sizes and row estimates |
| `/accounts/{id}/portfolio?history=false` | holdings valued in XLM/USD, totals, 24h/7d change, hourly value history |
| `/accounts/{id}/stats?hours` | indicators: volume, PnL, round trips, fees, first/last seen, heatmap, counterparties, largest transfers, flows, protocols |
| `/accounts/{id}/tags` | automatic tags (with reasons) + user labels |
| `/labels?tag&address`, `/labels/tags` | user labels; tag counts |
| `POST /labels` `{address, tag, note}`, `DELETE /labels/{address}/{tag}` | add/update or remove a label (Bearer `LABELS_TOKEN` when set) |
| `/leaderboards?hours&limit` | traders, arbitrageurs, active, fee_payers, pools, protocol_users, whales (rows carry `labels`) |
| `/alerts?hours&min_xlm&min_profit&min_move&kinds` | notable events: large_transfer, large_trade, flash_loan, arbitrage, price_move, new_asset |

Feeds use keyset pagination: pass the last row's position as `before`. That is the `id` for transactions and operations, and dot-separated position columns for the rest: `ledger.tx_index.transfer_index` for transfers, `ledger.tx_index.op_index.trade_index` for trades, `ledger.tx_index.op_index.event_index` for events.

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
| `LOGO_FETCH` | — (`cmd/indexer`) | Run the SEP-1 logo resolver next to the API (default `true`). |
| — | `Network`, `CursorName`, `UI` | Shown in `/status`; ingest cursor; embedded explorer (`/ui/`). |

## Probes

- `/health` (liveness): 200 when the process serves and Postgres answers.
- `/ready` (readiness): 200 when Postgres answers and, with `READY_MAX_LAG`, the index is fresh. Otherwise 503 with `reason` (and `lag_seconds`).
- `/status`: cursor, ledger range, pipeline component heartbeats (`components` table), gap count and the last gap (`ledger_gaps`), `rpc_tip`, `lag_ledgers`, `lag_seconds`.
