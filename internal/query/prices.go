package query

import (
	"context"
	"fmt"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
)

// Valuation inputs. Every asset is priced in XLM from the 1-minute candles of
// its pair against native (either orientation, since pairs are canonical).
// Candles are built from verified fills only, so spoofed pools cannot move
// prices. Prices are volume-weighted over the 6 hours before the reference
// time, falling back to the last close; assets with less than
// MinLiquidityXLM of volume against XLM in the prior 24h get no price at all
// (dust markets in spam tokens otherwise produce absurd valuations).

// MinLiquidityXLM is the 24h XLM volume an asset needs to be priced.
const MinLiquidityXLM = 250

// pxLegs is the pair-vs-native candle stream as (asset, bucket, px, xlm, units).
const pxLegs = `SELECT base AS asset, bucket, close AS px, counter_volume AS xlm, base_volume AS units FROM candles_1m WHERE counter = 'native'
	UNION ALL
	SELECT counter, bucket, CASE WHEN close > 0 THEN 1 / close END, base_volume, counter_volume FROM candles_1m WHERE base = 'native'`

// pxCTE prices every liquid asset (XLM per unit) over candles after the
// placeholder since; for SQL that values many rows at once.
func pxCTE(since string) string {
	return fmt.Sprintf(`px AS (
	SELECT asset, (sum(xlm) / nullif(sum(units), 0))::float8 AS p FROM (%s) x
	WHERE bucket > %s GROUP BY asset HAVING sum(xlm) >= %d
	UNION ALL SELECT 'native', 1)`, pxLegs, since, MinLiquidityXLM)
}

// XLMPrices returns XLM per unit for each liquid asset at time at (native = 1).
func (r *Repo) XLMPrices(ctx context.Context, assets []string, at time.Time) (analytics.Prices, error) {
	out := analytics.Prices{"native": 1}
	var want []string
	for _, a := range assets {
		if a != "native" && a != "" {
			want = append(want, a)
		}
	}
	if len(want) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `WITH a AS (SELECT DISTINCT unnest($1::text[]) AS asset)
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
		) l ON true`, want, at, MinLiquidityXLM)
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

// HourlyPrices returns XLM-per-unit hourly VWAPs since from, per asset.
func (r *Repo) HourlyPrices(ctx context.Context, assets []string, from time.Time) (analytics.HourlySeries, error) {
	out := analytics.HourlySeries{}
	if len(assets) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `SELECT asset, date_trunc('hour', bucket) AS h, (sum(xlm) / nullif(sum(units), 0))::float8
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
