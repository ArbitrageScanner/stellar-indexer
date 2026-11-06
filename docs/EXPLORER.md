# Explorer UI

The binary embeds a web explorer and serves it at **`/ui/`** from `run` and `api` (opening `/` in a browser redirects there; API clients still get the JSON index). The UI is plain ES modules and CSS in [`web/static`](../web/static): no build step, no npm dependencies, no CDN. It talks only to the JSON API below.

## Pages

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

## Development

`UI_DIR=web/static ./bin/indexer api` serves the files from disk, so edits show up on reload. `UI_ENABLED=false` turns the UI off. The design system is described in [DESIGN.md](DESIGN.md).

The browser smoke test opens every page on desktop and phone widths. It fails on JS errors, error boxes or horizontal overflow:

```bash
npm i --no-save playwright@1.56.1 && npx playwright install chromium
node web/e2e/smoke.mjs http://localhost:8080
```
