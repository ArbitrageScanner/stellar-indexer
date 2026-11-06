package query

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LogoIndex lists every asset with a stored issuer image: its key, the
// contract id of its Stellar Asset Contract when indexed (so C… references
// share the image), and a short content version for cache-busting URLs.
func (r *Repo) LogoIndex(ctx context.Context) (Rows, error) {
	return r.rows(ctx, `
		SELECT l.asset_key AS asset, a.contract_id AS contract, left(l.sha256, 12) AS v
		FROM asset_logos l
		JOIN assets a ON a.asset_key = l.asset_key
		WHERE l.status = 'ok' AND l.data IS NOT NULL
		ORDER BY l.asset_key`)
}

// Logo is one stored image.
type Logo struct {
	ContentType string
	Data        []byte
	SHA256      string
}

// Logo returns the stored image for an asset key (CODE:ISSUER) or for the
// contract id of its Stellar Asset Contract; nil when there is none.
func (r *Repo) Logo(ctx context.Context, key string) (*Logo, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var l Logo
	err := r.db.QueryRow(ctx, `
		SELECT l.content_type, l.data, l.sha256
		FROM asset_logos l
		JOIN assets a ON a.asset_key = l.asset_key
		WHERE (l.asset_key = $1 OR a.contract_id = $1) AND l.status = 'ok' AND l.data IS NOT NULL
		LIMIT 1`, key).Scan(&l.ContentType, &l.Data, &l.SHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}
