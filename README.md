# stellar-indexer

A Stellar ledger indexer for analytics, written in Go. It streams ledgers from Stellar RPC, decodes the full `LedgerCloseMeta` with the official [`go-stellar-sdk`](https://github.com/stellar/go-stellar-sdk), and writes analytics tables to Postgres. Classic operations, SDEX and AMM trades, token flows, Soroban contract events and DeFi protocols (Soroswap, Aquarius, Blend, CLMM pools) are all covered. A JSON API and a built-in web explorer sit on top.

## Live demo

> ### [power-explorer.stellar.ascn.ai](https://power-explorer.stellar.ascn.ai)
> The explorer running against **Stellar pubnet** with this repository's deployment setup ([deploy/](deploy/)).

| | |
|---|---|
| Explorer | [overview](https://power-explorer.stellar.ascn.ai/ui/) · [markets](https://power-explorer.stellar.ascn.ai/ui/#/markets) · [protocols](https://power-explorer.stellar.ascn.ai/ui/#/protocols) · [leaderboards](https://power-explorer.stellar.ascn.ai/ui/#/leaders) · [notable events](https://power-explorer.stellar.ascn.ai/ui/#/alerts) |
| API | [`/status`](https://power-explorer.stellar.ascn.ai/status) · [`/pairs`](https://power-explorer.stellar.ascn.ai/pairs) · [`/openapi.yaml`](https://power-explorer.stellar.ascn.ai/openapi.yaml) · [API playground](https://power-explorer.stellar.ascn.ai/ui/#/api) |

The demo indexes from the moment it was launched, with no historical backfill. It keeps about one day of transactions and events, and about a month of trades and protocol events. Label writes are disabled there.

## Features

- **Complete ledger decoding.** Ledgers, transactions, every operation type with typed details, CAP-67 token transfers, account, trustline, offer and pool state, and all Soroban contract events with topics and data decoded to JSON.
- **Markets.** Fills from the SDEX, classic AMMs and Soroban DEXs go onto one canonical pair per asset pair. 1-minute candles roll up to 1 day.
- **DeFi protocols.** Decoders for Soroswap, Aquarius, Blend, CLMM and generic AMM pools. Protocol data counts only when the contract's code is on an allowlist, so lookalike contracts cannot fake volume.
- **Address analytics.** Portfolio valued in XLM and USD with history, PnL, arbitrage round trips, activity heatmaps and counterparties. Automatic tags (whale, market maker, arbitrage bot, …) and user labels.
- **Leaderboards and notable events.** Top traders, bots, pools, whales, large transfers and swaps, flash loans, sharp price moves.
- **Explorer UI.** Plain ES modules with no build step and no CDN, embedded in the binary. It covers every section of the API, with a dark and a day theme, and works down to phone width.
- **Built for operations.** A Kafka/Redpanda queue between fetching and writing gives exactly-once effects. Restarts resume cleanly, and fact tables are partitioned with retention. The API ships an OpenAPI contract, Prometheus metrics and readiness probes.
- **Split deployment.** A public, database-free gateway serves the UI and the API. The indexer server stays hidden behind a tunnel that it opens itself.

## Architecture

```
                      Stellar RPC
                           │ getLedgers
                           ▼
 fetcher ──▶ Redpanda/Kafka topic ──▶ writer ──▶ Postgres ◀── api ◀── gateway ◀── browser
          raw LedgerCloseMeta XDR      decode, verify,           JSON,  public edge:
          one partition                one transaction           UI     UI, cache, rate limits
```

| Process | Role |
|---|---|
| `indexer fetch` | Stateless. Streams ledgers from RPC onto the topic. Resumes at `max(topic end, DB cursor) + 1` |
| `indexer write` | Decodes, verifies protocol contracts and commits each batch with the cursor in one transaction. Kafka offsets are committed afterwards, so redelivery is a no-op |
| `indexer api` | Read side: JSON API, `/metrics`, `/ready` and the explorer. Scales horizontally |
| `indexer run` | Fetch, write and API in one process without a queue, for small setups |
| `gateway` | Separate binary and image: the public edge in front of an internal `indexer api` |

Indexing starts at the current ledger. History is out of scope by design, and any component can restart without losing or duplicating data. Design decisions and failure behaviour are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick start

```bash
docker compose up -d --build --wait  # postgres, redpanda, fetcher, writer, api on testnet
open http://localhost:8080/          # explorer
curl localhost:8080/status           # cursor, component heartbeats, gaps
```

Single process without a queue: `docker compose --profile direct up -d --build postgres indexer`.

Without Docker (Go 1.27):

```bash
make build
export DATABASE_URL=postgres://indexer:indexer@localhost:5432/indexer?sslmode=disable STELLAR_NETWORK=testnet
./bin/indexer run                                     # direct mode, or queue mode:
KAFKA_BROKERS=localhost:19092 ./bin/indexer fetch &
KAFKA_BROKERS=localhost:19092 ./bin/indexer write &
./bin/indexer api
```

Other commands: `indexer migrate` applies migrations and exits. `indexer inspect [seq]` decodes one ledger from RPC without a database. `indexer contract-code C…` shows a contract's code and which protocol allowlists it matches.

## Configuration

All settings are environment variables, validated at startup. [.env.example](.env.example) documents every variable. The main ones:

| Variable | Default | |
|---|---|---|
| `STELLAR_NETWORK` | `testnet` | `testnet`, `pubnet`, or custom (`NETWORK_PASSPHRASE` + `STELLAR_RPC_URL`) |
| `DATABASE_URL` | — | Postgres connection string |
| `KAFKA_BROKERS` | — | required by `fetch` and `write` |
| `RETAIN_RAW_LEDGERS` / `RETAIN_MARKET_LEDGERS` / `RETAIN_CANDLE_LEDGERS` | `0` (keep) | retention in ledgers (~5.5 s each); drops whole partitions |
| `INDEX_FAILED_TXS` | `true` | set `false` on pubnet to save space |
| `PROTOCOL_VERIFY` / `PROTOCOL_ALLOWLIST_FILE` | `enforce` / — | protocol contract verification |
| `API_KEYS`, `RATE_LIMIT_RPS`, `CORS_ORIGINS`, `CACHE_TTL` | —, `20`, `*`, `10s` | API access and limits |
| `LABELS_TOKEN` / `LABELS_OPEN` | — / `false` | who may write user labels |
| `LOGO_FETCH` | `true` | resolve issuer logos from SEP-1 `stellar.toml` |

### Pubnet sizing

Measured on pubnet:

| | Testnet | Pubnet |
|---|---|---|
| Database growth, all features | ~25 KB/ledger | ~1.2 MB/ledger (~18 GB/day) |
| Latency after ledger close | 2–5 s | 3–5 s |

About 80% of pubnet transactions are failed arbitrage attempts. A lean pubnet setup:

```
INDEX_FAILED_TXS=false
RETAIN_RAW_LEDGERS=20000       # ~1 day of transactions, operations, transfers and events
RETAIN_MARKET_LEDGERS=518400   # ~30 days of trades and protocol events
```

## Deployment

- **One server:** [docker-compose.yml](docker-compose.yml) runs the whole queue-mode stack.
- **Public edge + hidden indexer:** [deploy/README.md](deploy/README.md) covers Docker on both sides over WireGuard or an SSH tunnel with mTLS. A Kubernetes (k3s) variant comes with CI/CD to GHCR.

## Development

```bash
make test                            # unit and fixture tests on recorded pubnet ledgers
make itest DB=postgres://...         # + Postgres integration and the queue pipeline (in-memory Kafka)
make race                            # the same with the race detector
make lint                            # golangci-lint
make invariants                      # data-integrity checks against a running index
make e2e URL=http://localhost:8080   # browser smoke test of every explorer page (Playwright)
```

`UI_DIR=web/static ./bin/indexer api` serves the explorer from disk, so changes show up on reload. CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs lint, tests with the race detector and Postgres, the image builds and, on `main`, the full stack against testnet followed by the browser smoke test.

## Documentation

| | |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | packages, design decisions, failure behaviour, observability |
| [docs/DATA.md](docs/DATA.md) | what is indexed and how prices, protocols, portfolios and tags are computed |
| [docs/EXPLORER.md](docs/EXPLORER.md) | explorer pages, UI development and the browser smoke test |
| [docs/API.md](docs/API.md) | endpoint reference, API layering, middleware, probes |
| [internal/api/openapi.yaml](internal/api/openapi.yaml) | the API contract, also served at `/openapi.yaml` |
| [deploy/README.md](deploy/README.md) | split deployment and its verification |
| [docs/DESIGN.md](docs/DESIGN.md) | the explorer's design system |

## Limitations

- **No history.** Indexing starts at launch. A Galexie datastore behind `source.Source` is the plug-in point for backfill.
- **Query-time analytics.** Leaderboards, alerts and protocol summaries are computed from the fact tables behind a short cache. Long pubnet retention calls for pre-aggregated rollups or a ClickHouse consumer on the topic.
- **Partial state.** Account, trustline and offer tables hold the entries touched since indexing began, not a full ledger snapshot. Soroban token balances are net flows since launch.
- **Token decimals.** Volumes and prices assume 7 decimals, which holds for classic assets and most Soroban tokens.
- **Allowlists.** When a protocol upgrades its contracts, the new code hashes must be added. Until then their events are stored as unverified.
- **Unsupported DEXs.** Phoenix and other DEXs with one-field-per-event swap logs are not turned into trades. Their events are still stored.

## License

[MIT](LICENSE). Bundled third-party assets keep their own terms:

- The Wix Madefor fonts in `web/static/fonts` are under the SIL Open Font License 1.1 ([OFL.txt](web/static/fonts/OFL.txt)).
- The asset and protocol logos in `web/static/logos` are trademarks of their owners and are included only to identify those assets and protocols.
