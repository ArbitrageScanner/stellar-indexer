# stellar-indexer

A Stellar ledger indexer for analytics, written in Go. It reads ledgers from Stellar RPC (`getLedgers`), decodes the full `LedgerCloseMeta` XDR with the official `go-stellar-sdk` (v0.8.0, protocol 30), and writes analytics tables to Postgres. A JSON HTTP API serves queries over those tables, and a built-in web explorer (at `/ui/`) visualises them.

What it indexes:

| Area | Tables | Source |
|---|---|---|
| Ledgers / network | `ledgers` | ledger header plus tx-set counters (tx ok/failed, ops, Soroban share, fees) |
| Transactions | `transactions` | envelope, result, fee, memo, Soroban resources, invoked contract and function |
| Operations | `operations` | every op type, with typed `details` jsonb (payments, offers, path payments, invoke args, …) |
| Prices | `trades`, `candles_1m` | SDEX fills and classic AMM fills (ClaimAtoms), plus Soroban DEX swaps; 1m OHLCV rolled up to 5m…1d at query time |
| Token flows | `transfers` | SDK `token_transfer` processor (CAP-67 semantics): transfer / mint / burn / clawback (and fees, optional) for classic assets and SAC/SEP-41 tokens |
| Accounts | `accounts`, `trustlines`, `liquidity_pools`, `offers` | compacted ledger-entry changes per ledger (latest state of every entry touched) |
| Assets | `assets` | classic assets, their SAC contract ids, and custom Soroban tokens |
| Soroban | `contract_events` | all non-token contract events, with topics and data decoded to JSON |
| Protocols | `protocol_events` | decoded **Soroswap**, **Aquarius**, **Blend**, CLMM pools, generic AMMs |

## Architecture

```
RPC ─▶ fetcher ─▶ Redpanda/Kafka topic (raw LedgerCloseMeta, 1 partition) ─▶ writer ─▶ Postgres ◀─ api replicas (+ UI)
```

- **fetcher** (`indexer fetch`): stateless. Streams ledgers from RPC onto the topic and resumes after the newest ledger already on the topic (or the DB cursor).
- **writer** (`indexer write`): decodes, verifies protocol contracts, and commits each batch together with the cursor in one Postgres transaction. Kafka offsets are committed only afterwards, and ledgers at or below the cursor are skipped, so delivery is effectively exactly-once.
- **api** (`indexer api`): read side, JSON + explorer UI. Scales horizontally.
- **direct mode** (`indexer run`): the same pipeline in one process without a queue, for small setups.

Design decisions, failure behaviour and package layout are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). The API design (layers, middleware, security) is in [docs/API.md](docs/API.md), and its contract in [`internal/api/openapi.yaml`](internal/api/openapi.yaml) (served at `/openapi.yaml`).

**Split deployment** (hidden indexer server, public edge): the `gateway` image serves the UI and proxies the API over a WireGuard/SSH tunnel that the indexer server initiates. The indexer server needs no inbound ports, and the edge holds no database credentials. See [deploy/README.md](deploy/README.md).

Indexing starts at the current ledger (`START_LEDGER=latest`). History is deliberately out of scope; restarts of any component resume where they stopped.

## Quick start

```bash
cp .env.example .env                 # optional
docker compose up -d --build --wait  # postgres, redpanda, fetcher, writer, api (testnet)
curl localhost:8080/status           # cursor, component heartbeats, gaps
curl localhost:8080/ready            # 200 when the index is fresh
open http://localhost:8080/          # explorer UI
```

Single process without a queue: `docker compose --profile direct up -d --build postgres indexer`.

Without Docker (Go 1.27):

```bash
make build
export DATABASE_URL=postgres://indexer:indexer@localhost:5432/indexer?sslmode=disable STELLAR_NETWORK=testnet
./bin/indexer run                                         # direct mode
KAFKA_BROKERS=localhost:19092 ./bin/indexer fetch &       # queue mode
KAFKA_BROKERS=localhost:19092 ./bin/indexer write &
./bin/indexer api
```

| Command | Purpose |
|---|---|
| `indexer fetch` | RPC → Kafka topic |
| `indexer write` | Kafka topic → decode → verify → Postgres (+ retention). Extra writers in the group are hot standbys |
| `indexer api` | HTTP API, `/metrics`, `/ready`, explorer UI; any number of replicas |
| `indexer run` | Direct pipeline + API in one process (no queue) |
| `indexer migrate` | Apply database migrations and exit |
| `indexer inspect [seq]` | Decode one ledger from RPC and print a summary (no DB) |
| `indexer contract-code C…` | Show contract executables and which protocol allowlists they match |
| `gateway` (separate binary/image) | Public edge: UI + API proxy to an internal `indexer api` (deploy/README.md) |

## Explorer UI

The binary embeds a web explorer and serves it at **`/ui/`** from `run` and `api` (opening `/` in a browser redirects there; API clients still get the JSON index). The UI is plain ES modules and CSS in [`web/static`](web/static): no build step, no npm dependencies, no CDN. It talks only to the JSON API below.

| Section | What it shows |
|---|---|
| **Overview** | latest ledger, ingestion lag, tx/ops throughput, success rate, Soroban share, fees (1h/24h/7d); stacked tx chart; ops and Soroban-share charts; live ledger feed; operation mix; top markets; protocol activity; table sizes |
| **Ledgers / Transactions / Operations** | paginated feeds with filters (status, Soroban/classic, source, contract, function, op type); ledger and tx detail with operations rendered in plain language, token transfers, trades, decoded protocol and contract events, and raw JSON |
| **Accounts** | top XLM holders; account page with balances, trustlines, offers, 24h activity, and tabs for operations, transfers, trades and DeFi activity |
| **Transfers** | CAP-67 transfer/mint/burn/clawback feed, filterable by asset and type |
| **Markets / Trades** | all pairs (filter by code and venue); pair page with candlestick or line chart (1m–1d candles, 6h–30d ranges, volume pane, crosshair tooltip), 24h ticker, invert, venue split, top takers, live trades; global trades feed |
| **Assets / Liquidity pools** | most active assets; asset page with 24h mint/burn/transfer totals, top pairs, holders, transfers; classic AMM pools with reserves and spot price |
| **Protocols** | Aquarius, Soroswap, Blend, CLMM, generic AMMs: per-protocol cards; protocol page with hourly activity, actions, Blend lending by asset (supply/borrow/repay/flash loans), top pools, contracts, users, and a paginated event feed |
| **Contracts / Events** | most active contracts (calls + events); contract page with functions, topics, protocol actions, events and invocations; global event browser (filter by protocol, `unattributed`, topic, contract) |
| **Address portfolio** | every address page: net worth in USD/XLM, 24h/7d change, hourly value-over-time, an allocation donut, and holdings (XLM, trustlines, Soroban tokens, classic LP shares split into underlyings, Blend supply/debt) with price, value, change and share |
| **Indicators** | per address, for a 24h/7d/30d window: XLM-valued trading volume, realized and unrealized PnL (average cost), arbitrage round trips and their profit, fees, tx success, first and last seen, a weekday × hour activity heatmap, venue and protocol mix, top counterparties, largest transfers, top pairs, net flows by asset |
| **Tags** | automatic tags with an explanation on hover (whale, market maker, arbitrage bot, high-frequency, mostly failing, issuer, token or protocol contract, LP, borrower, flash loans, DeFi user, new account, dormant); user labels (`#tag` + note) stored server-side, searchable from the global search, shown on leaderboards |
| **Leaderboards** | top traders, arbitrage bots, most active accounts, fee payers, pools, DeFi power users and whales, for 1h/24h/7d |
| **Notable events** | large transfers and swaps (XLM-valued), flash loans, profitable arbitrage, sharp price moves on liquid XLM pairs, new assets; also as a widget on the overview |
| **Watchlist / Compare** | follow addresses (stored in the browser; import everyone with a `#tag`) with live net worth, tags and 24h activity; compare two addresses side by side, including shared assets and counterparties |
| **API playground** | every endpoint, with examples filled from live data; run requests and see status, timing and size; copy as curl / JavaScript / Python |

Also: global search (ledger, tx hash, account, contract, asset code; press `/`), a live status pill, a light/dark/auto theme, responsive layout down to phone width, loading/empty/error states with retry, and stellar.expert links for every entity. The API endpoint is configurable in settings (⚙), so the same UI can point at any indexer (CORS is open).

Development: `UI_DIR=web/static ./bin/indexer api` serves the files from disk, so edits show up on reload. `UI_ENABLED=false` turns the UI off. Browser smoke test (opens every page on desktop and mobile, and fails on JS errors, error boxes or horizontal overflow):

```bash
npm i --no-save playwright@1.56.1 && npx playwright install chromium
node web/e2e/smoke.mjs http://localhost:8080
```

### How portfolio values and tags are computed

- **Prices.** Each asset is priced in XLM from its 1-minute candles against XLM: a VWAP over the previous 6 hours, falling back to the last close. An asset gets **no price** unless at least 250 XLM traded against XLM in the prior 24h, so spam tokens with dust markets cannot inflate a portfolio; they are listed as "illiquid". XLM is priced in USD through `USD_ASSET` (Circle USDC on testnet and pubnet by default).
- **Balances.** XLM, trustlines and pool shares come from indexed ledger state (accounts touched since indexing began). Soroban token balances, and every balance of a `C…` contract address, are net token transfers since indexing began, flagged *approx.* Classic pool shares are valued as `shares / total_shares × reserves`. Blend positions are net supply − withdraw and borrow − repay events.
- **History.** Current balances are walked back hour by hour through the address's transfers and valued at hourly VWAPs. Fees, pool shares and Blend positions are held constant.
- **PnL.** Average-cost accounting in XLM over the window's fills, each valued by its XLM leg (or at current prices). Sales without a cost basis inside the window realise nothing.
- **Arbitrage.** A round trip is a single transaction in which one taker's first sold asset is also their last bought asset. Its profit is the net of that asset.
- **Automatic tags.** Thresholds are in `internal/api/insights.go` and listed on the Tags page: whale ≥ 1M XLM, large holder ≥ 100K XLM, arbitrage bot ≥ 3 round trips in 7 days, market maker = two-sided offers or ≥ 100 offer updates in 24h, high-frequency ≥ 500 txs or ≥ 1000 fills in 24h, new account < 7 days (creation ledger = starting sequence number >> 32), dormant = no activity for 7 days.
- **User labels** live in the `labels` table. Writes are open by default, which suits a local tool. Set `LABELS_TOKEN` to require `Authorization: Bearer <token>`; the UI asks for it under settings (⚙). Tags match `[a-z0-9][a-z0-9_.-]{0,31}`; notes are limited to 280 characters.

## Ingestion and reliability

- **Source.** `RPCLedgerBackend` fetches `getLedgers` in batches (`RPC_BUFFER`) and long-polls at the tip.
- **Queue contract.** One record per ledger: key = 8-byte sequence, value = `LedgerCloseMeta` XDR (zstd), headers `schema=lcm-xdr-v1`, `network=<passphrase hash>`, `closed_at`. The producer is idempotent with `acks=all`, and the fetcher advances only after each acknowledgement. The writer rejects records from another network or schema.
- **Exactly-once effect.** Data, cursor, gap records and the writer heartbeat are committed in one Postgres transaction. The writer commits Kafka offsets after that transaction; anything at or below the cursor is a no-op on redelivery.
- **Restarts.** The fetcher resumes at `max(topic end, DB cursor) + 1`, the writer at its committed offset, and `run` at the DB cursor. If a component is down longer than the RPC retention window, the missing range goes to `ledger_gaps` (visible in `/status` and `indexer_ledger_gaps_total`) and ingestion continues.
- **Integrity.** Each ledger's `prev_hash` must match the stored predecessor; otherwise the writer stops (`ErrChainMismatch`). Undecodable ledgers also stop it rather than being skipped. DB and RPC errors retry the same batch with backoff.
- **Partitions and retention.** Fact tables are partitioned by ledger range (`PARTITION_LEDGERS`). Retention (`RETAIN_*_LEDGERS`) detaches and drops whole partitions concurrently, so there are no row deletes.
- **Verified test:** `internal/ingest/pipeline_test.go` covers fetcher restart without duplicates, redelivery without double-counted candles, foreign-record rejection, gap recording, chain-mismatch stops and partition retention. A chaos run against real Redpanda and Postgres (`kill -9` of the fetcher and writer, Redpanda and Postgres stopped mid-stream) ended with every check in [`scripts/check-invariants.sql`](scripts/check-invariants.sql) passing (see below).

### Protocol verification

Protocol events are recognised by their shape, which any contract can imitate. Each pool contract's executable is therefore resolved: from contract-instance changes in ingested ledgers, otherwise once via RPC `getLedgerEntries`, cached in `contract_code`. Fills and events are `verified` only when the wasm hash is allowlisted for the protocol. Unverified data is stored and marked in the API/UI, but never reaches candles, prices, PnL, leaderboards or alerts.

- Default allowlists (pubnet and testnet) cover code shared by many factory-deployed pools of Aquarius, Soroswap, Blend and CLMM, plus the Aquarius and Soroswap routers ([internal/verify/allowlist.go](internal/verify/allowlist.go)). One-off "generic AMM" contracts are not allowlisted.
- Extend or replace them with `PROTOCOL_ALLOWLIST_FILE`; review candidates with `indexer contract-code C…`. `PROTOCOL_VERIFY=off` disables the check.
- A failed lookup retries the batch, so a transient RPC error never marks genuine data unverified.

### Prices

Every fill becomes a `trades` row, recorded from the taker's side (`sold_*` = what the taker gave). It is projected onto a canonical pair `(base, counter)` with `base < counter` lexicographically. `price` is counter per base. The API flips orientation on request (`/price?base=native&counter=USDC:G...`). Asset keys are `native`, `CODE:ISSUER`, or a `C...` contract id for custom tokens. SAC contract ids are mapped to their classic key, so an Aquarius XLM/USDC swap and an SDEX XLM/USDC fill land on the same pair.

Venues:
- `sdex`: order-book fills from manage offer and path payment results
- `amm`: classic liquidity pools
- `soroswap`: pair `swap` events (string topic `"SoroswapPair"`)
- `aquarius`: pool `trade(token_in, token_out, user) -> [in, out, fee]`
- `clmm`: Uniswap-v3-style `swap{amount0, amount1, sqrt_price_x96}`
- `amm_generic`: `swap{amount_in, amount_out}`

When an event doesn't name its tokens (Soroswap pairs, CLMM), the tokens are inferred from token transfers into and out of the pool contract in the same operation.

Volumes assume 7 decimals, which holds for all classic assets and most Soroban tokens.

### Protocol decoding

Event shapes were taken from live pubnet traffic, not from docs:

| Protocol | Recognised by | Stored as |
|---|---|---|
| Soroswap | topic0 = string `SoroswapPair` / `SoroswapRouter` / `SoroswapFactory` | `swap`, `sync`, `deposit`, `withdraw`, `Router.swap`, … |
| Aquarius | `trade` with 3 address topics; router `(swap\|deposit\|withdraw, [tokens], user)` | `trade`, `router_swap`, `update_reserves`, `pool_state`, `claim_fees`, … |
| Blend | `(supply\|withdraw\|borrow\|repay\|supply_collateral\|withdraw_collateral, asset, user) -> [amount, b/dTokens]`, `flash_loan` | action, user, asset, `amount0`=underlying, `amount1`=b/dTokens |
| CLMM | `swap` with `amount0/amount1/sqrt_price_x96` | `swap` with signed amounts |

Once a contract is attributed to a protocol, its other events are labelled with that protocol too (auctions, admin calls, …). Unknown contracts still have every event in `contract_events`, so you can add new decoders later from stored data.

## HTTP API

The contract is [`internal/api/openapi.yaml`](internal/api/openapi.yaml) (served at `/openapi.yaml`; a test keeps it in sync with the routes). Middleware, security and probes are described in [docs/API.md](docs/API.md). The read API is public by default; set `API_KEYS` to require a key. Label writes are closed unless `LABELS_TOKEN` or `LABELS_OPEN` is set. Requests are rate-limited per client IP, and heavy analytics responses are cached for `CACHE_TTL`. Analytics windows end at the newest indexed ledger (`index_now()`). Prices, candles, PnL, leaderboards and alerts use verified fills only.

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

## Configuration

All settings are environment variables, validated at startup (an invalid combination exits with code 2). [.env.example](.env.example) documents every variable. The main ones:

| Var | Default | |
|---|---|---|
| `STELLAR_NETWORK` | `testnet` | `testnet`, `pubnet`, or custom (set `NETWORK_PASSPHRASE` + `STELLAR_RPC_URL`) |
| `START_LEDGER` | `latest` | used only when there is nothing to resume from |
| `KAFKA_BROKERS` | — | required by `fetch`/`write`; topic `stellar.<network>.ledgers` (`KAFKA_TOPIC`) |
| `KAFKA_RETENTION` / `KAFKA_RETENTION_BYTES` | `24h` / unlimited (compose: 2 GiB) | outage buffer and replay window |
| `PARTITION_LEDGERS` | `20000` | ledger range per partition, fixed at first start |
| `RETAIN_RAW_LEDGERS` / `RETAIN_MARKET_LEDGERS` / `RETAIN_CANDLE_LEDGERS` | `0` (keep) | retention windows in ledgers (~5.5 s each); raw and market windows drop whole partitions |
| `INDEX_FAILED_TXS` | `true` | set `false` on pubnet to save space |
| `PROTOCOL_VERIFY` / `PROTOCOL_ALLOWLIST_FILE` | `enforce` / — | protocol contract verification |
| `API_KEYS`, `RATE_LIMIT_RPS`, `CORS_ORIGINS`, `CACHE_TTL`, `READY_MAX_LAG` | —, `20`, `*`, `10s`, `2m` | API hardening (docs/API.md) |
| `LABELS_TOKEN` / `LABELS_OPEN` | — / `false` | label write policy |
| `METRICS_ADDR` | `:9100` | `/metrics` and `/health` of `fetch` and `write` |
| `LOG_FORMAT` | `text` | `json` for log shipping (compose default) |

### Pubnet sizing (measured)

| | Testnet | Pubnet |
|---|---|---|
| DB growth (all features) | ~25 KB/ledger | ~1.2 MB/ledger (**~18 GB/day**) |
| Backfill speed via public RPC | ~6 ledgers/s | ~2.8 ledgers/s (chain makes ~0.18/s) |
| Live latency after close | 2–5 s | 3–5 s |

On pubnet, roughly 80% of transactions are failed arbitrage attempts. For a lean pubnet deployment:

```
INDEX_FAILED_TXS=false
RETAIN_RAW_LEDGERS=20000       # ~1 day of txs/ops/transfers/events (one partition)
RETAIN_MARKET_LEDGERS=518400   # ~30 days of trades/protocol events
# candles and ledgers kept forever
```

## Tests

```bash
make test                     # unit + fixture tests on recorded pubnet ledgers
make itest                    # + Postgres integration and the queue pipeline (in-memory Kafka); DB=postgres://...
make race                     # the same with the race detector
make lint                     # golangci-lint (.golangci.yml)
make invariants               # data-integrity checks against a running index
make e2e URL=http://localhost:8080   # browser smoke test of every explorer page (needs playwright)
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs lint, `go test -race` with Postgres, a Docker build, and, on main, the full compose stack against testnet followed by the browser smoke test.

- **Fixtures.** `internal/processor/testdata` holds two recorded pubnet ledgers:
  - 65311337: a Blend flash-loan arbitrage through four Aquarius pools, plus SDEX and AMM fills.
  - 65312802: Soroswap pair swaps.
- **Pipeline tests** (`internal/ingest`): queue round trip, fetcher resume without duplicates, idempotent redelivery (no double-counted candles), foreign-record rejection, gap recording, chain-mismatch stop, and partition retention that never resurrects pruned partitions.
- **Verification tests** (`internal/verify`): an allowlisted pool versus a contract with the same event shape but foreign code; an in-ledger upgrade; resolver failure aborts instead of marking data unverified.
- **API tests** (`internal/api`, `internal/analytics`): every endpoint against fixtures, middleware (rate limit, API keys, CORS, request IDs, cache, label write policy), OpenAPI coverage of all routes, and pure domain logic (PnL, round trips, valuation, tags).

## Limitations

- **No history (by design).** Indexing starts at launch. A Galexie datastore (`BufferedStorageBackend`) behind `source.Source` would be the plug-in point for backfill.
- **Query-time analytics.** Leaderboards, alerts and protocol summaries are computed from the fact tables (verified rows) behind a short cache. For long pubnet retention, pre-aggregated rollups or a ClickHouse consumer on the topic are the next step (docs/ARCHITECTURE.md).
- **Allowlist maintenance.** When a protocol upgrades its contracts, new code hashes must be allowlisted. Until then their events are stored as unverified and counted in `indexer_unverified_protocol_events_total`.
- **Account state.** Account, trustline and offer tables hold every entry touched since indexing began, not a full ledger snapshot. A full snapshot would mean bootstrapping from a history-archive checkpoint (`ingest.CheckpointChangeReader`), which runs to tens of GB on pubnet.
- **Contract balances.** Soroban contract-data balances (SEP-41 balances held by contracts) are not materialised. Token flows are in `transfers`.
- **Token decimals.** Soroban tokens with decimals other than 7 get correct raw amounts in `trades`, but wrong `price`/`*_volume` values.
- **Unsupported DEXs.** Phoenix and other DEXs with one-field-per-event swap logs aren't turned into trades. Their events are still stored in `contract_events`.
