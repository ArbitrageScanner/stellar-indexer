-- Issuer-published asset images (SEP-1 stellar.toml [[CURRENCIES]] image),
-- resolved by internal/logos. Only raster images vouched for by the issuer's
-- home domain are stored; status none/error rows record why there is none.
CREATE TABLE asset_logos (
    asset_key    text PRIMARY KEY,
    home_domain  text NOT NULL DEFAULT '',
    image_url    text NOT NULL DEFAULT '',
    content_type text NOT NULL DEFAULT '',
    data         bytea,
    sha256       text NOT NULL DEFAULT '',
    status       text NOT NULL CHECK (status IN ('ok', 'none', 'error')),
    detail       text NOT NULL DEFAULT '',
    checked_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (status <> 'ok' OR (data IS NOT NULL AND octet_length(data) <= 102400 AND sha256 <> ''))
);
CREATE INDEX asset_logos_checked ON asset_logos (checked_at);
