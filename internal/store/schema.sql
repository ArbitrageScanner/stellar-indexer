-- Stellar analytics indexer schema (idempotent).

CREATE TABLE IF NOT EXISTS cursors (
    name        text PRIMARY KEY,
    ledger      bigint NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ledgers (
    sequence          bigint PRIMARY KEY,
    hash              text NOT NULL,
    prev_hash         text NOT NULL,
    closed_at         timestamptz NOT NULL,
    protocol          int NOT NULL,
    tx_count          int NOT NULL,
    tx_success        int NOT NULL,
    tx_failed         int NOT NULL,
    op_count          int NOT NULL,
    soroban_tx_count  int NOT NULL,
    base_fee          int NOT NULL,
    base_reserve      int NOT NULL,
    total_coins       bigint NOT NULL,
    fee_pool          bigint NOT NULL,
    fees_charged      bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS ledgers_closed_at ON ledgers (closed_at);

CREATE TABLE IF NOT EXISTS transactions (
    hash          text PRIMARY KEY,
    ledger        bigint NOT NULL,
    tx_index      int NOT NULL,
    id            bigint NOT NULL,
    closed_at     timestamptz NOT NULL,
    source        text NOT NULL,
    fee_account   text NOT NULL,
    seq_num       bigint NOT NULL,
    fee_charged   bigint NOT NULL,
    max_fee       bigint NOT NULL,
    op_count      int NOT NULL,
    successful    boolean NOT NULL,
    result_code   text NOT NULL,
    memo_type     text NOT NULL,
    memo          text NOT NULL,
    is_soroban    boolean NOT NULL,
    contract_id   text,
    function      text,
    resource_fee  bigint NOT NULL DEFAULT 0,
    instructions  bigint NOT NULL DEFAULT 0,
    inner_hash    text
);
CREATE INDEX IF NOT EXISTS transactions_ledger ON transactions (ledger, tx_index);
CREATE INDEX IF NOT EXISTS transactions_source ON transactions (source, id DESC);
CREATE INDEX IF NOT EXISTS transactions_contract ON transactions (contract_id, id DESC) WHERE contract_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS operations (
    id           bigint PRIMARY KEY,
    tx_hash      text NOT NULL,
    ledger       bigint NOT NULL,
    tx_index     int NOT NULL,
    op_index     int NOT NULL,
    closed_at    timestamptz NOT NULL,
    type         text NOT NULL,
    source       text NOT NULL,
    successful   boolean NOT NULL,
    result_code  text NOT NULL,
    details      jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS operations_tx ON operations (tx_hash);
CREATE INDEX IF NOT EXISTS operations_source ON operations (source, id DESC);
CREATE INDEX IF NOT EXISTS operations_type ON operations (type, ledger);

CREATE TABLE IF NOT EXISTS assets (
    asset_key    text PRIMARY KEY,
    type         text NOT NULL,
    code         text,
    issuer       text,
    contract_id  text,
    first_seen   bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS assets_contract ON assets (contract_id);
CREATE INDEX IF NOT EXISTS assets_code ON assets (code);

CREATE TABLE IF NOT EXISTS trades (
    ledger         bigint NOT NULL,
    tx_index       int NOT NULL,
    op_index       int NOT NULL,
    trade_index    int NOT NULL,
    tx_hash        text NOT NULL,
    closed_at      timestamptz NOT NULL,
    venue          text NOT NULL,
    pool           text NOT NULL,
    maker          text NOT NULL,
    taker          text NOT NULL,
    sold_asset     text NOT NULL,
    sold_amount    numeric NOT NULL,
    bought_asset   text NOT NULL,
    bought_amount  numeric NOT NULL,
    base           text NOT NULL,
    counter        text NOT NULL,
    price          double precision NOT NULL,
    base_volume    double precision NOT NULL,
    counter_volume double precision NOT NULL,
    PRIMARY KEY (ledger, tx_index, op_index, trade_index)
);
CREATE INDEX IF NOT EXISTS trades_pair ON trades (base, counter, closed_at DESC);
CREATE INDEX IF NOT EXISTS trades_closed ON trades (closed_at DESC);
CREATE INDEX IF NOT EXISTS trades_taker ON trades (taker, closed_at DESC);
CREATE INDEX IF NOT EXISTS trades_venue ON trades (venue, closed_at DESC);

-- 1-minute OHLCV per canonical pair (base < counter). Larger intervals are
-- rolled up at query time.
CREATE TABLE IF NOT EXISTS candles_1m (
    base            text NOT NULL,
    counter         text NOT NULL,
    bucket          timestamptz NOT NULL,
    open            double precision NOT NULL,
    high            double precision NOT NULL,
    low             double precision NOT NULL,
    close           double precision NOT NULL,
    base_volume     double precision NOT NULL,
    counter_volume  double precision NOT NULL,
    trades          int NOT NULL,
    PRIMARY KEY (base, counter, bucket)
);
CREATE INDEX IF NOT EXISTS candles_bucket ON candles_1m (bucket DESC);

CREATE TABLE IF NOT EXISTS transfers (
    ledger          bigint NOT NULL,
    tx_index        int NOT NULL,
    transfer_index  int NOT NULL,
    op_index        int,
    tx_hash         text NOT NULL,
    closed_at       timestamptz NOT NULL,
    type            text NOT NULL,
    from_addr       text,
    to_addr         text,
    asset           text NOT NULL,
    contract_id     text,
    amount          numeric NOT NULL,
    PRIMARY KEY (ledger, tx_index, transfer_index)
);
CREATE INDEX IF NOT EXISTS transfers_from ON transfers (from_addr, ledger DESC);
CREATE INDEX IF NOT EXISTS transfers_to ON transfers (to_addr, ledger DESC);
CREATE INDEX IF NOT EXISTS transfers_asset ON transfers (asset, closed_at DESC);

CREATE TABLE IF NOT EXISTS contract_events (
    ledger       bigint NOT NULL,
    tx_index     int NOT NULL,
    op_index     int NOT NULL,
    event_index  int NOT NULL,
    tx_hash      text NOT NULL,
    closed_at    timestamptz NOT NULL,
    contract_id  text NOT NULL,
    topic0       text NOT NULL,
    topics       jsonb NOT NULL,
    data         jsonb,
    protocol     text,
    successful   boolean NOT NULL,
    PRIMARY KEY (ledger, tx_index, op_index, event_index)
);
CREATE INDEX IF NOT EXISTS contract_events_contract ON contract_events (contract_id, ledger DESC);
CREATE INDEX IF NOT EXISTS contract_events_topic0 ON contract_events (topic0, ledger DESC);

CREATE TABLE IF NOT EXISTS protocol_events (
    ledger       bigint NOT NULL,
    tx_index     int NOT NULL,
    op_index     int NOT NULL,
    event_index  int NOT NULL,
    tx_hash      text NOT NULL,
    closed_at    timestamptz NOT NULL,
    protocol     text NOT NULL,
    contract_id  text NOT NULL,
    action       text NOT NULL,
    user_addr    text,
    asset        text,
    amount0      numeric,
    amount1      numeric,
    data         jsonb,
    PRIMARY KEY (ledger, tx_index, op_index, event_index)
);
CREATE INDEX IF NOT EXISTS protocol_events_proto ON protocol_events (protocol, action, closed_at DESC);
CREATE INDEX IF NOT EXISTS protocol_events_user ON protocol_events (user_addr, closed_at DESC);
CREATE INDEX IF NOT EXISTS protocol_events_contract ON protocol_events (contract_id, closed_at DESC);

-- Current state of entries touched since indexing began (not a full snapshot).
CREATE TABLE IF NOT EXISTS accounts (
    account_id      text PRIMARY KEY,
    balance         bigint NOT NULL,
    buying_liab     bigint NOT NULL,
    selling_liab    bigint NOT NULL,
    seq_num         bigint NOT NULL,
    num_subentries  int NOT NULL,
    num_sponsoring  int NOT NULL,
    num_sponsored   int NOT NULL,
    flags           int NOT NULL,
    home_domain     text NOT NULL,
    master_weight   int NOT NULL,
    num_signers     int NOT NULL,
    sponsor         text,
    last_modified   bigint NOT NULL,
    deleted         boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS accounts_balance ON accounts (balance DESC) WHERE NOT deleted;

CREATE TABLE IF NOT EXISTS trustlines (
    account_id     text NOT NULL,
    asset          text NOT NULL,
    balance        bigint NOT NULL,
    trust_limit    bigint NOT NULL,
    buying_liab    bigint NOT NULL,
    selling_liab   bigint NOT NULL,
    flags          int NOT NULL,
    last_modified  bigint NOT NULL,
    PRIMARY KEY (account_id, asset)
);
CREATE INDEX IF NOT EXISTS trustlines_asset ON trustlines (asset, balance DESC);

CREATE TABLE IF NOT EXISTS liquidity_pools (
    pool_id          text PRIMARY KEY,
    asset_a          text NOT NULL,
    asset_b          text NOT NULL,
    reserve_a        bigint NOT NULL,
    reserve_b        bigint NOT NULL,
    total_shares     bigint NOT NULL,
    trustline_count  bigint NOT NULL,
    fee_bp           int NOT NULL,
    last_modified    bigint NOT NULL,
    deleted          boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS offers (
    offer_id       bigint PRIMARY KEY,
    seller         text NOT NULL,
    selling        text NOT NULL,
    buying         text NOT NULL,
    amount         bigint NOT NULL,
    price_n        int NOT NULL,
    price_d        int NOT NULL,
    price          double precision NOT NULL,
    passive        boolean NOT NULL,
    last_modified  bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS offers_pair ON offers (selling, buying, price);

-- User-defined address labels (tags), written through the API.
CREATE TABLE IF NOT EXISTS labels (
    address     text NOT NULL,
    tag         text NOT NULL,
    note        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (address, tag)
);
CREATE INDEX IF NOT EXISTS labels_tag ON labels (tag);
