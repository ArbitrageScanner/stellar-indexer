# Data and methodology

What the indexer stores, and how the analytics on top of it are derived.

## Indexed data

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
| Service | `cursors`, `ledger_gaps`, `components`, `contract_code`, `labels`, `asset_logos` | ingest position, missed ranges, heartbeats, resolved contract code, user labels, issuer logos |

All tables are created by the migrations in [`internal/store/migrations`](../internal/store/migrations). Fact tables (`transactions`, `operations`, `trades`, `transfers`, `contract_events`, `protocol_events`) are partitioned by ledger range; see ADR-6 in [ARCHITECTURE.md](ARCHITECTURE.md).

## Prices and venues

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

## Protocol decoding

Event shapes were taken from live pubnet traffic, not from docs:

| Protocol | Recognised by | Stored as |
|---|---|---|
| Soroswap | topic0 = string `SoroswapPair` / `SoroswapRouter` / `SoroswapFactory` | `swap`, `sync`, `deposit`, `withdraw`, `Router.swap`, … |
| Aquarius | `trade` with 3 address topics; router `(swap\|deposit\|withdraw, [tokens], user)` | `trade`, `router_swap`, `update_reserves`, `pool_state`, `claim_fees`, … |
| Blend | `(supply\|withdraw\|borrow\|repay\|supply_collateral\|withdraw_collateral, asset, user) -> [amount, b/dTokens]`, `flash_loan` | action, user, asset, `amount0`=underlying, `amount1`=b/dTokens |
| CLMM | `swap` with `amount0/amount1/sqrt_price_x96` | `swap` with signed amounts |

Once a contract is attributed to a protocol, its other events are labelled with that protocol too (auctions, admin calls, …). Unknown contracts still have every event in `contract_events`, so you can add new decoders later from stored data.

## Protocol verification

Protocol events are recognised by their shape, which any contract can imitate. Each pool contract's executable is therefore resolved: from contract-instance changes in ingested ledgers, otherwise once via RPC `getLedgerEntries`, cached in `contract_code`. Fills and events are `verified` only when the wasm hash is allowlisted for the protocol. Unverified data is stored and marked in the API/UI, but never reaches candles, prices, PnL, leaderboards or alerts.

- Default allowlists (pubnet and testnet) cover code shared by many factory-deployed pools of Aquarius, Soroswap, Blend and CLMM, plus the Aquarius and Soroswap routers ([internal/verify/allowlist.go](../internal/verify/allowlist.go)). One-off "generic AMM" contracts are not allowlisted.
- Extend or replace them with `PROTOCOL_ALLOWLIST_FILE`; review candidates with `indexer contract-code C…`. `PROTOCOL_VERIFY=off` disables the check.
- A failed lookup retries the batch, so a transient RPC error never marks genuine data unverified.

## Portfolio values and tags

- **Prices.** Each asset is priced in XLM from its 1-minute candles against XLM: a VWAP over the previous 6 hours, falling back to the last close. An asset gets **no price** unless at least 250 XLM traded against XLM in the prior 24h, so spam tokens with dust markets cannot inflate a portfolio; they are listed as "illiquid". XLM is priced in USD through `USD_ASSET` (Circle USDC on testnet and pubnet by default).
- **Balances.** XLM, trustlines and pool shares come from indexed ledger state (accounts touched since indexing began). Soroban token balances, and every balance of a `C…` contract address, are net token transfers since indexing began, flagged *approx.* Classic pool shares are valued as `shares / total_shares × reserves`. Blend positions are net supply − withdraw and borrow − repay events.
- **History.** Current balances are walked back hour by hour through the address's transfers and valued at hourly VWAPs. Fees, pool shares and Blend positions are held constant.
- **PnL.** Average-cost accounting in XLM over the window's fills, each valued by its XLM leg (or at current prices). Sales without a cost basis inside the window realise nothing.
- **Arbitrage.** A round trip is a single transaction in which one taker's first sold asset is also their last bought asset. Its profit is the net of that asset.
- **Automatic tags.** Thresholds are in `internal/analytics/tags.go` and listed on the Tags page: whale ≥ 1M XLM, large holder ≥ 100K XLM, arbitrage bot ≥ 3 round trips in 7 days, market maker = two-sided offers or ≥ 100 offer updates in 24h, high-frequency ≥ 500 txs or ≥ 1000 fills in 24h, new account < 7 days (creation ledger = starting sequence number >> 32), dormant = no activity for 7 days.
- **User labels** live in the `labels` table. Writes are closed by default. Set `LABELS_TOKEN` to accept `Authorization: Bearer <token>` (the UI asks for it under settings, ⚙), or `LABELS_OPEN=true` for a local tool. Tags match `[a-z0-9][a-z0-9_.-]{0,31}`; notes are limited to 280 characters.

## Test fixtures

`internal/processor/testdata` holds two recorded pubnet ledgers that the decoder tests run on:
- 65311337: a Blend flash-loan arbitrage through four Aquarius pools, plus SDEX and AMM fills.
- 65312802: Soroswap pair swaps.
