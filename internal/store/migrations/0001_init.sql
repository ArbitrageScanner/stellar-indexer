-- 0001: baseline schema. Append-only fact tables are RANGE-partitioned by
-- ledger; partitions are created ahead of the writer and dropped whole by
-- retention (see partitions.go). State tables are small and unpartitioned.

CREATE TABLE meta (
    key    text PRIMARY KEY,
    value  text NOT NULL
);

CREATE TABLE cursors (
    name        text PRIMARY KEY,
    ledger      bigint NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Heartbeats of pipeline components (fetcher, writer) for /status and /ready.
CREATE TABLE components (
    name        text PRIMARY KEY,
    role        text NOT NULL,
    ledger      bigint,
    tip         bigint,
    info        jsonb NOT NULL DEFAULT '{}',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Ranges the writer never received (downtime longer than the RPC window).
CREATE TABLE ledger_gaps (
    from_ledger  bigint PRIMARY KEY,
    to_ledger    bigint NOT NULL,
    reason       text NOT NULL,
    detected_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledgers (
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
CREATE INDEX ledgers_closed_at ON ledgers (closed_at);

-- Analytics windows are anchored to the newest indexed ledger, not the wall
-- clock, so a paused or lagging indexer still answers "last 24h" correctly.
CREATE FUNCTION index_now() RETURNS timestamptz LANGUAGE sql STABLE AS
    $$ SELECT coalesce(max(closed_at), now()) FROM ledgers $$;

CREATE TABLE transactions (
    hash          text NOT NULL,
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
    inner_hash    text,
    PRIMARY KEY (hash, ledger)
) PARTITION BY RANGE (ledger);
CREATE INDEX transactions_ledger ON transactions (ledger, tx_index);
CREATE INDEX transactions_id ON transactions (id DESC);
CREATE INDEX transactions_source ON transactions (source, id DESC);
CREATE INDEX transactions_fee_account ON transactions (fee_account, ledger);
CREATE INDEX transactions_contract ON transactions (contract_id, id DESC) WHERE contract_id IS NOT NULL;
CREATE INDEX transactions_inner_hash ON transactions (inner_hash) WHERE inner_hash IS NOT NULL;

CREATE TABLE operations (
    id           bigint NOT NULL,
    tx_hash      text NOT NULL,
    ledger       bigint NOT NULL,
    tx_index     int NOT NULL,
    op_index     int NOT NULL,
    closed_at    timestamptz NOT NULL,
    type         text NOT NULL,
    source       text NOT NULL,
    successful   boolean NOT NULL,
    result_code  text NOT NULL,
    details      jsonb NOT NULL,
    PRIMARY KEY (id, ledger)
) PARTITION BY RANGE (ledger);
CREATE INDEX operations_id ON operations (id DESC);
CREATE INDEX operations_tx ON operations (tx_hash);
CREATE INDEX operations_source ON operations (source, id DESC);
CREATE INDEX operations_type ON operations (type, id DESC);

CREATE TABLE assets (
    asset_key    text PRIMARY KEY,
    type         text NOT NULL,
    code         text,
    issuer       text,
    contract_id  text,
    first_seen   bigint NOT NULL
);
CREATE INDEX assets_contract ON assets (contract_id);
CREATE INDEX assets_code ON assets (code);
CREATE INDEX assets_issuer ON assets (issuer);

-- verified = classic venue, or a Soroban pool whose code is allowlisted for
-- the protocol. Only verified fills feed candles and prices.
CREATE TABLE trades (
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
    verified       boolean NOT NULL,
    PRIMARY KEY (ledger, tx_index, op_index, trade_index)
) PARTITION BY RANGE (ledger);
CREATE INDEX trades_pair ON trades (base, counter, closed_at DESC);
CREATE INDEX trades_closed ON trades (closed_at DESC);
CREATE INDEX trades_taker ON trades (taker, closed_at DESC);
CREATE INDEX trades_venue ON trades (venue, closed_at DESC);
CREATE INDEX trades_tx ON trades (tx_hash);

-- 1-minute OHLCV per canonical pair (base < counter), verified fills only.
CREATE TABLE candles_1m (
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
CREATE INDEX candles_bucket ON candles_1m (bucket DESC);
CREATE INDEX candles_counter ON candles_1m (counter, bucket DESC);

CREATE TABLE transfers (
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
) PARTITION BY RANGE (ledger);
CREATE INDEX transfers_from ON transfers (from_addr, ledger DESC);
CREATE INDEX transfers_to ON transfers (to_addr, ledger DESC);
CREATE INDEX transfers_asset ON transfers (asset, closed_at DESC);
CREATE INDEX transfers_tx ON transfers (tx_hash);

CREATE TABLE contract_events (
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
    verified     boolean NOT NULL DEFAULT false,
    successful   boolean NOT NULL,
    PRIMARY KEY (ledger, tx_index, op_index, event_index)
) PARTITION BY RANGE (ledger);
CREATE INDEX contract_events_contract ON contract_events (contract_id, ledger DESC);
CREATE INDEX contract_events_topic0 ON contract_events (topic0, ledger DESC);
CREATE INDEX contract_events_tx ON contract_events (tx_hash);
CREATE INDEX contract_events_closed ON contract_events (closed_at DESC);

CREATE TABLE protocol_events (
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
    verified     boolean NOT NULL,
    PRIMARY KEY (ledger, tx_index, op_index, event_index)
) PARTITION BY RANGE (ledger);
CREATE INDEX protocol_events_proto ON protocol_events (protocol, action, closed_at DESC);
CREATE INDEX protocol_events_user ON protocol_events (user_addr, closed_at DESC);
CREATE INDEX protocol_events_contract ON protocol_events (contract_id, closed_at DESC);
CREATE INDEX protocol_events_closed ON protocol_events (closed_at DESC);
CREATE INDEX protocol_events_tx ON protocol_events (tx_hash);

-- Executable of every contract seen with protocol-shaped events: the input to
-- protocol verification (wasm hash allowlists).
CREATE TABLE contract_code (
    contract_id  text PRIMARY KEY,
    executable   text NOT NULL,      -- wasm | stellar_asset | missing
    wasm_hash    text,
    ledger       bigint NOT NULL,    -- ledger of the change (0 = resolved via RPC)
    checked_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contract_code_wasm ON contract_code (wasm_hash);

-- Current state of entries touched since indexing began (not a full snapshot).
CREATE TABLE accounts (
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
CREATE INDEX accounts_balance ON accounts (balance DESC) WHERE NOT deleted;

CREATE TABLE trustlines (
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
CREATE INDEX trustlines_asset ON trustlines (asset, balance DESC);

CREATE TABLE liquidity_pools (
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

CREATE TABLE offers (
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
CREATE INDEX offers_pair ON offers (selling, buying, price);
CREATE INDEX offers_seller ON offers (seller);

-- User-defined address labels, written through the API.
CREATE TABLE labels (
    address     text NOT NULL,
    tag         text NOT NULL,
    note        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (address, tag)
);
CREATE INDEX labels_tag ON labels (tag);
