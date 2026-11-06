package logos

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	rpc "github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// PG is the Postgres Store over the asset_logos table.
type PG struct{ DB *pgxpool.Pool }

func (p PG) Due(ctx context.Context, limit int, okTTL, retryTTL time.Duration) ([]Asset, error) {
	rows, err := p.DB.Query(ctx, `
		SELECT a.asset_key, a.code, a.issuer
		FROM assets a
		LEFT JOIN asset_logos l ON l.asset_key = a.asset_key
		WHERE a.issuer IS NOT NULL AND a.code IS NOT NULL AND a.code <> ''
		  AND (l.asset_key IS NULL
		       OR (l.status = 'ok' AND l.checked_at < now() - make_interval(secs => $2))
		       OR (l.status <> 'ok' AND l.checked_at < now() - make_interval(secs => $3)))
		ORDER BY l.checked_at NULLS FIRST, a.first_seen DESC
		LIMIT $1`, limit, okTTL.Seconds(), retryTTL.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.Key, &a.Code, &a.Issuer); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p PG) HomeDomains(ctx context.Context, issuers []string) (map[string]string, error) {
	out := map[string]string{}
	rows, err := p.DB.Query(ctx, `SELECT account_id, home_domain FROM accounts
		WHERE account_id = ANY($1) AND NOT deleted AND home_domain <> ''`, issuers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, d string
		if err := rows.Scan(&id, &d); err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, rows.Err()
}

// Save upserts a result. A transient error never discards an image already
// stored: the row keeps it (and its ok status) and is retried later.
func (p PG) Save(ctx context.Context, r Result) error {
	if r.Status == StatusError {
		_, err := p.DB.Exec(ctx, `
			INSERT INTO asset_logos (asset_key, home_domain, status, detail, checked_at)
			VALUES ($1, $2, 'error', $3, now())
			ON CONFLICT (asset_key) DO UPDATE SET detail = EXCLUDED.detail, checked_at = now(),
				status = CASE WHEN asset_logos.status = 'ok' THEN 'ok' ELSE 'error' END`,
			r.Key, r.HomeDomain, r.Detail)
		return err
	}
	var data any
	if r.Status == StatusOK {
		data = r.Data
	}
	_, err := p.DB.Exec(ctx, `
		INSERT INTO asset_logos (asset_key, home_domain, image_url, content_type, data, sha256, status, detail, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (asset_key) DO UPDATE SET home_domain = EXCLUDED.home_domain, image_url = EXCLUDED.image_url,
			content_type = EXCLUDED.content_type, data = EXCLUDED.data, sha256 = EXCLUDED.sha256,
			status = EXCLUDED.status, detail = EXCLUDED.detail, checked_at = now()`,
		r.Key, r.HomeDomain, r.ImageURL, r.ContentType, data, r.SHA256, r.Status, r.Detail)
	return err
}

// RPCLookup returns a DomainLookup that reads issuer accounts from Stellar RPC
// with one getLedgerEntries call per batch.
func RPCLookup(client *rpc.Client) DomainLookup {
	return func(ctx context.Context, issuers []string) (map[string]string, error) {
		out := map[string]string{}
		keys := make([]string, 0, len(issuers))
		for _, id := range issuers {
			acc, err := xdr.AddressToAccountId(id)
			if err != nil {
				continue
			}
			k := xdr.LedgerKey{Type: xdr.LedgerEntryTypeAccount, Account: &xdr.LedgerKeyAccount{AccountId: acc}}
			b64, err := k.MarshalBinaryBase64()
			if err != nil {
				return out, err
			}
			keys = append(keys, b64)
		}
		if len(keys) == 0 {
			return out, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		resp, err := client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
		if err != nil {
			return out, err
		}
		for _, e := range resp.Entries {
			var data xdr.LedgerEntryData
			if err := xdr.SafeUnmarshalBase64(e.DataXDR, &data); err != nil || data.Account == nil {
				continue
			}
			if d := string(data.Account.HomeDomain); d != "" {
				out[data.Account.AccountId.Address()] = d
			}
		}
		return out, nil
	}
}
