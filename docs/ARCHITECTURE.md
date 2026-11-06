# Architecture

```
               ┌───────────┐  raw LedgerCloseMeta   ┌────────────────────┐
 Stellar RPC ─▶│  fetcher  │──── (XDR, zstd) ──────▶│ Redpanda / Kafka   │
 getLedgers    │ (stateless)│   key = ledger seq    │ topic: 1 partition │
               └───────────┘                        └─────────┬──────────┘
                     ▲ resume = max(topic end, DB cursor) + 1 │ consumer group
                     │                                        ▼
               ┌─────┴──────────────────────────────────────────────────────┐
               │ writer: decode (processor) → verify (contract code) →      │
               │ one Postgres transaction per batch: data + cursor + gaps   │
               │ + heartbeat; Kafka offsets committed after the DB commit   │
               └──────────────────────────────┬─────────────────────────────┘
                                              ▼
                                   Postgres (fact tables partitioned by ledger)
                                              ▲
                          api replicas (JSON API, /metrics, /ready, explorer UI)
```

`indexer run` provides the same pipeline in one process without a queue, for
small deployments and development (`docker compose --profile direct`).

## Packages

| Package | Responsibility |
|---|---|
| `internal/source` | RPC ledger source (`getLedgers`, live tail) |
| `internal/processor` | Pure decoding: `LedgerCloseMeta` → `Batch` (rows); no I/O |
| `internal/verify` | Protocol verification: contract code resolution and allowlists |
| `internal/queue` | Kafka topic contract, producer and consumer |
| `internal/ingest` | Pipeline loops: `Fetcher`, `QueueWriter`, direct `Runner` |
| `internal/store` | Migrations, partitions, idempotent batch writes, retention |
| `internal/query`, `internal/analytics`, `internal/api` | Read side: SQL repository, domain logic, HTTP transport (see `docs/API.md`) |
| `internal/metrics` | The Prometheus registry and every exported metric |
| `internal/httpx` | HTTP building blocks shared by the API and the gateway (request IDs, CORS, security headers, keys, rate limit, cache) |
| `internal/gateway`, `cmd/gateway` | Public edge for split deployments (ADR-10) |
| `internal/logos` | SEP-1 asset images: issuer home_domain → stellar.toml → validated, re-encoded PNG (ADR-11) |
| `cmd/indexer` | Wiring and process lifecycle only |

## Decisions

### ADR-1: a queue between fetching and writing
The fetcher and the writer fail independently. RPC outages, DB maintenance and
writer deploys no longer stall each other: the topic buffers up to
`KAFKA_RETENTION` (24h by default, bounded by `KAFKA_RETENTION_BYTES`). Writers
can be redeployed or scaled to hot standbys (the consumer group gives the
partition to one of them). New consumers, such as a ClickHouse sink or an
alerting service, can attach without touching ingestion.

### ADR-2: raw XDR on the bus, not decoded rows
The record value is the ledger's `LedgerCloseMeta` XDR, the immutable source of
truth. A decoder fix can be applied to everything still on the topic by
resetting the writer group's offsets and the cursor. Decoded rows would bake
the current decoder's bugs into the bus. Cost: the writer decodes (cheap
compared with the DB write). Records carry `schema=lcm-xdr-v1` and a network
tag, and the writer rejects foreign records.

### ADR-3: one partition, at-least-once delivery, idempotent sink
Ledgers must be applied in order (candles, state upserts). One ledger every ~5s
is far below a partition's capacity, so the topic has a single partition. The
producer is idempotent with `acks=all` and waits for each acknowledgement. The
writer commits offsets only after the DB transaction. The store skips every
ledger at or below its cursor, and the cursor moves in the same transaction as
the data. Redelivery after any crash is therefore a no-op: the pipeline is
effectively exactly-once. This is covered by `internal/ingest/pipeline_test.go`
and a chaos run (README).

### ADR-4: index from launch; restarts resume, they never re-scan
History is out of scope by design (`START_LEDGER=latest`). Every component
resumes from durable state: the fetcher from `max(topic end, DB cursor) + 1`,
the writer from its committed offset (with skips below the cursor), and `run`
from the DB cursor. If a component is down longer than the RPC retention
window, the missing range is recorded in `ledger_gaps`, exposed on `/status`,
and counted in `indexer_ledger_gaps_total`. Ingestion is not silently
"healed".

### ADR-5: chain continuity is enforced
Each new ledger's `prev_hash` must equal the stored hash of its predecessor.
Stellar has no reorgs, so a mismatch means the source served inconsistent
data. The writer stops (`ErrChainMismatch`, `indexer_chain_mismatch_total`)
rather than index it.

### ADR-6: partition fact tables by ledger range
`transactions`, `operations`, `trades`, `transfers`, `contract_events` and
`protocol_events` are `RANGE (ledger)` partitioned (`PARTITION_LEDGERS`, 20000
≈ 30h; fixed at first use in `meta`). Partitions are created ahead of the
writer, outside the write transaction. Retention detaches them `CONCURRENTLY`
and drops them, with no row deletes, bloat or vacuum storms. Small tables
(`ledgers`, `candles_1m`, state) are not partitioned. Retention granularity is
one partition, and config validation rejects windows smaller than that.

### ADR-7: protocol attribution requires allowlisted contract code
Protocol events are recognised by shape, which anyone can imitate. The verifier
resolves each pool contract's executable: from contract-instance changes in
ingested ledgers, otherwise once via RPC `getLedgerEntries`, cached in
`contract_code`. An event or fill is `verified` only if that wasm hash is
allowlisted for the protocol. Unverified data is stored and visible, but never
feeds candles or prices. A failed lookup aborts the batch, which is retried, so
genuine data is never stored as unverified because of a transient RPC error.
Default allowlists cover code shared by many factory-deployed pools plus each
protocol's router. They are extended with `PROTOCOL_ALLOWLIST_FILE`, using
`indexer contract-code` to review new code.

### ADR-8: analytics windows anchor to the index, not the wall clock
`index_now()` (the newest indexed close time) anchors every "last N hours"
query, so a paused or lagging indexer still answers consistently.

### ADR-9: versioned, checksummed migrations
`internal/store/migrations/NNNN_name.sql` are embedded, applied in order, each
in its own transaction, under an advisory lock (safe with concurrent
api/fetch/write starts), and verified by checksum. A database newer than the
binary, or created by the pre-migration prototype, is refused.

### ADR-10: public edge separated from the indexer
For deployments where the indexer server must stay off the internet, the
read side splits in two (`deploy/`). A stateless `gateway` (its own binary
and image, with no database, Kafka or Stellar code) serves the UI and proxies
the API. The internal `indexer api` runs on the private server and is
reachable only through a tunnel that the private server initiates
(WireGuard, or a reverse SSH tunnel). It is protected by mTLS and an internal
key. The gateway owns the public concerns (TLS via Caddy, per-client rate
limits, CORS, security headers, an edge cache, blocking internal endpoints),
replaces client-supplied forwarding headers, and never exposes upstream
error details. Shared HTTP building blocks live in `internal/httpx`, so both
sides apply the same rules. A compromised edge yields no database access and
no data beyond the public read API.

### ADR-11: asset logos are vouched for, re-encoded and served same-origin
The explorer shows two kinds of logo. Protocol marks, XLM and the assets of
the StellarExpert Top 50 SEP-42 list are bundled in `web/static/logos` and
matched by exact `CODE:ISSUER` or SAC contract id (never by code, which
anyone can issue). Every other classic asset is resolved by
`internal/logos`, which runs in the `api` process (`LOGO_FETCH`). It reads
the issuer's `home_domain` from the index or, in one batched
`getLedgerEntries` call, from RPC; fetches `stellar.toml`; and accepts the
image only when a `[[CURRENCIES]]` entry lists this exact code and issuer.
Downloads go through a client that dials only public unicast addresses after
DNS resolution, follows at most three https redirects and caps the body at
2 MB. The image must sniff as PNG, JPEG, GIF or WebP (never SVG). It is
checked for its pixel count before decoding, then decoded and re-encoded as a
PNG of at most 128 px, so only pixels this process produced are stored
(`asset_logos`) and served from `/assets/{key}/logo` on the explorer's own
origin. The UI CSP keeps `img-src 'self'`, and the browser never contacts
issuer hosts. Rows are rechecked weekly (daily after none/error), and a
transient failure never drops a stored image.

## Failure behaviour

| Failure | Behaviour |
|---|---|
| RPC down or slow | Fetcher retries with backoff and resumes from the topic end; the writer idles; the API serves (stale, `/ready` reports lag) |
| Kafka down | Fetcher retries producing (it advances only on ack); the writer retries polling |
| Postgres down | Writer retries the same batch (offsets not committed); the fetcher keeps filling the topic; the API returns 503 on `/ready` |
| Fetcher or writer crash (`kill -9`) | Restart resumes; duplicates are skipped by the cursor check |
| Downtime > RPC retention | Gap recorded in `ledger_gaps`; ingestion continues from the RPC's oldest ledger |
| Inconsistent source (`prev_hash`) | Writer stops with `ErrChainMismatch` |
| Undecodable ledger | Writer stops (`indexer_process_errors_total`), so data is never skipped silently |
| Foreign or garbage record on the topic | Ignored and counted (`indexer_queue_rejected_total`) |
| RPC error during code lookup | Batch retried; never stored unverified by mistake |

## Observability
Every process exports Prometheus metrics: the API at `/metrics`, fetch and
write at `METRICS_ADDR` (default `:9100`). Key series:
`indexer_ledger_close_lag_seconds`, `indexer_queue_consumer_lag`,
`indexer_write_duration_seconds`, `indexer_ledger_gaps_total`,
`indexer_chain_mismatch_total`, `indexer_unverified_protocol_events_total`,
`http_request_duration_seconds`. Components write heartbeats to `components`,
which `/status` exposes. Logs are structured (`LOG_FORMAT=json`).

## Known limits
- No historical backfill (by design, ADR-4).
- Analytics are computed from the fact tables at query time, behind a short
  response cache. At pubnet scale with long retention, pre-aggregated rollups
  (per-hour trader, pool and protocol tables maintained by the writer, or a
  ClickHouse consumer on the topic) are the next step.
- Balances outside indexed ledger state (Soroban tokens, contract addresses)
  and Blend positions are net flows since indexing began.
- Allowlists need maintenance when protocols upgrade their code. Until an
  operator adds the new hash, events fail closed: they are stored unverified,
  logged and counted.
