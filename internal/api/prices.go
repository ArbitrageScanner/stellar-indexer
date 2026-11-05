package api

import (
	"context"
	"time"
)

// Valuation helpers. Every asset is priced in XLM from the 1-minute candles of
// its pair against native (either orientation, since pairs are canonical);
// XLM is priced in USD through the configured USD asset (USDC by default).
// Prices are volume-weighted over the 6 hours before the reference time, which
// keeps dust fills at absurd prices from dominating, with the last close as a
// fallback within the day. Assets with less than minLiquidityXLM of volume
// against XLM in the prior 24h get no price at all: dust markets in spam
// tokens otherwise produce absurd valuations.

const minLiquidityXLM = 250

// pxLegs is the pair-vs-native candle stream as (asset, bucket, xlm, units).
const pxLegs = `SELECT base AS asset, bucket, close AS px, counter_volume AS xlm, base_volume AS units FROM candles_1m WHERE counter = 'native'
	UNION ALL
	SELECT counter, bucket, CASE WHEN close > 0 THEN 1 / close END, base_volume, counter_volume FROM candles_1m WHERE base = 'native'`

// xlmPrices returns XLM per unit for each asset at time at (native = 1).
func (s *Server) xlmPrices(ctx context.Context, assets []string, at time.Time) (map[string]float64, error) {
	out := map[string]float64{"native": 1}
	var want []string
	for _, a := range assets {
		if a != "native" && a != "" {
			want = append(want, a)
		}
	}
	if len(want) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `WITH a AS (SELECT DISTINCT unnest($1::text[]) AS asset)
		SELECT a.asset, CASE WHEN liq.xlm >= $3 THEN coalesce(v.p, l.p) END::float8 FROM a
		LEFT JOIN LATERAL (
		  SELECT coalesce(sum(xlm), 0) AS xlm FROM (
		    SELECT counter_volume AS xlm FROM candles_1m
		      WHERE base = a.asset AND counter = 'native' AND bucket > $2::timestamptz - interval '24 hours' AND bucket <= $2
		    UNION ALL
		    SELECT base_volume FROM candles_1m
		      WHERE base = 'native' AND counter = a.asset AND bucket > $2::timestamptz - interval '24 hours' AND bucket <= $2) x
		) liq ON true
		LEFT JOIN LATERAL (
		  SELECT sum(xlm) / nullif(sum(units), 0) AS p FROM (
		    SELECT counter_volume AS xlm, base_volume AS units FROM candles_1m
		      WHERE base = a.asset AND counter = 'native' AND bucket > $2::timestamptz - interval '6 hours' AND bucket <= $2
		    UNION ALL
		    SELECT base_volume, counter_volume FROM candles_1m
		      WHERE base = 'native' AND counter = a.asset AND bucket > $2::timestamptz - interval '6 hours' AND bucket <= $2) x
		) v ON true
		LEFT JOIN LATERAL (
		  SELECT p FROM (
		    (SELECT close AS p, bucket FROM candles_1m WHERE base = a.asset AND counter = 'native' AND bucket <= $2 ORDER BY bucket DESC LIMIT 1)
		    UNION ALL
		    (SELECT 1 / nullif(close, 0), bucket FROM candles_1m WHERE base = 'native' AND counter = a.asset AND bucket <= $2 ORDER BY bucket DESC LIMIT 1)
		  ) y ORDER BY bucket DESC LIMIT 1
		) l ON true`, want, at, minLiquidityXLM)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var p *float64
		if err := rows.Scan(&a, &p); err != nil {
			return nil, err
		}
		if p != nil && *p > 0 {
			out[a] = *p
		}
	}
	return out, rows.Err()
}

// usdPerXLM returns the USD value of one XLM at time at, or 0 when the USD
// asset has no market against XLM in the index.
func (s *Server) usdPerXLM(ctx context.Context, at time.Time) float64 {
	if s.USDAsset == "" {
		return 0
	}
	p, err := s.xlmPrices(ctx, []string{s.USDAsset}, at)
	if err != nil || p[s.USDAsset] == 0 {
		return 0
	}
	return 1 / p[s.USDAsset]
}

// hourlyPrices returns XLM-per-unit hourly VWAPs since from, per asset.
func (s *Server) hourlyPrices(ctx context.Context, assets []string, from time.Time) (map[string]map[time.Time]float64, error) {
	out := map[string]map[time.Time]float64{}
	rows, err := s.db.Query(ctx, `SELECT asset, date_trunc('hour', bucket) AS h, (sum(xlm) / nullif(sum(units), 0))::float8
		FROM (`+pxLegs+`) x WHERE asset = ANY($1) AND bucket >= $2 GROUP BY 1, 2`, assets, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var h time.Time
		var p *float64
		if err := rows.Scan(&a, &h, &p); err != nil {
			return nil, err
		}
		if p == nil || *p <= 0 {
			continue
		}
		if out[a] == nil {
			out[a] = map[time.Time]float64{}
		}
		out[a][h.UTC()] = *p
	}
	return out, rows.Err()
}

// latestPricesCTE prices every asset traded against XLM in the last 24h
// (XLM per unit), for use in SQL that values many rows at once.
const latestPricesCTE = `px AS (
	SELECT asset, (sum(xlm) / nullif(sum(units), 0))::float8 AS p FROM (` + pxLegs + `) x
	WHERE bucket > now() - interval '24 hours' GROUP BY asset HAVING sum(xlm) >= 250
	UNION ALL SELECT 'native', 1)`
