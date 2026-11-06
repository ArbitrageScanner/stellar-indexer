// Package logos resolves issuer-published asset images (SEP-1).
//
// For each classic asset CODE:ISSUER the resolver reads the issuer account's
// home_domain (from the index, falling back to RPC), fetches
// https://<home_domain>/.well-known/stellar.toml, and accepts an image only
// when a [[CURRENCIES]] entry lists this exact code and issuer: the domain
// has to vouch for the issuer, and the issuer for the domain. The image is
// downloaded through an SSRF-guarded client, size-capped, checked by its
// magic bytes (PNG, JPEG, GIF, WebP; never SVG, since it is served from the
// explorer's own origin), decoded and re-encoded as a PNG of at most 128 px,
// and stored in asset_logos, so the browser never talks to third-party hosts
// and only pixels this process produced are ever served.
package logos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/stellar/go-stellar-sdk/clients/stellartoml"
)

// Result states stored per asset.
const (
	StatusOK    = "ok"    // an image is stored
	StatusNone  = "none"  // the issuer publishes no (valid) image for this asset
	StatusError = "error" // transient failure (network, RPC); retried sooner
)

// MaxImageBytes caps a stored (re-encoded) image.
const MaxImageBytes = 100 << 10

// Asset is a classic asset due for (re)resolution.
type Asset struct {
	Key, Code, Issuer string
}

// Result is the outcome for one asset.
type Result struct {
	Key         string
	Status      string
	HomeDomain  string
	ImageURL    string
	ContentType string
	Data        []byte
	SHA256      string
	Detail      string
}

// Store persists results and supplies work.
type Store interface {
	// Due returns up to limit assets never resolved, or resolved before the
	// TTL of their status.
	Due(ctx context.Context, limit int, okTTL, retryTTL time.Duration) ([]Asset, error)
	// HomeDomains returns the indexed home_domain of each known issuer
	// (accounts the index has seen; empty values are omitted).
	HomeDomains(ctx context.Context, issuers []string) (map[string]string, error)
	Save(ctx context.Context, r Result) error
}

// DomainLookup returns the home_domain of issuer accounts the index does not
// know (e.g. via RPC getLedgerEntries). Missing accounts are omitted.
type DomainLookup func(ctx context.Context, issuers []string) (map[string]string, error)

// TomlFetcher fetches a domain's stellar.toml.
type TomlFetcher interface {
	GetStellarToml(domain string) (*stellartoml.Response, error)
}

// Resolver runs the resolution loop.
type Resolver struct {
	Store    Store
	Lookup   DomainLookup // optional
	Toml     TomlFetcher
	HTTP     *http.Client // image downloads; use SafeClient in production
	Log      *slog.Logger
	Batch    int           // assets per step
	Every    time.Duration // pause between steps
	OKTTL    time.Duration // recheck interval for a stored image
	RetryTTL time.Duration // recheck interval for none/error

	// checkURL validates an image URL before download; tests relax it.
	checkURL func(string) error
}

// New returns a resolver with production defaults and an SSRF-guarded client.
func New(store Store, lookup DomainLookup, log *slog.Logger) *Resolver {
	hc := SafeClient(15 * time.Second)
	return &Resolver{
		Store: store, Lookup: lookup, Log: log, HTTP: hc,
		Toml:  &stellartoml.Client{HTTP: hc},
		Batch: 25, Every: 30 * time.Second, OKTTL: 7 * 24 * time.Hour, RetryTTL: 24 * time.Hour,
	}
}

// Run resolves due assets until ctx ends.
func (r *Resolver) Run(ctx context.Context) {
	r.Log.Info("logo resolver started", "batch", r.Batch, "every", r.Every)
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := r.Step(ctx)
		if err != nil && ctx.Err() == nil {
			r.Log.Warn("logo resolver step failed", "err", err)
		}
		wait := r.Every
		if n == r.Batch {
			wait = 2 * time.Second // backlog: keep going, gently
		}
		t.Reset(wait)
	}
}

// Step resolves one batch and returns how many assets it handled.
func (r *Resolver) Step(ctx context.Context) (int, error) {
	due, err := r.Store.Due(ctx, r.Batch, r.OKTTL, r.RetryTTL)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	domains, lookupErr := r.domains(ctx, due)
	tomls := map[string]tomlResult{}
	for _, a := range due {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		res := r.resolve(ctx, a, domains, lookupErr, tomls)
		if err := r.Store.Save(ctx, res); err != nil {
			return 0, fmt.Errorf("save %s: %w", a.Key, err)
		}
		if res.Status == StatusOK {
			r.Log.Debug("logo stored", "asset", a.Key, "domain", res.HomeDomain, "bytes", len(res.Data))
		}
	}
	return len(due), nil
}

type tomlResult struct {
	resp *stellartoml.Response
	err  error
}

func (r *Resolver) domains(ctx context.Context, due []Asset) (map[string]string, error) {
	issuers := make([]string, 0, len(due))
	seen := map[string]bool{}
	for _, a := range due {
		if !seen[a.Issuer] {
			seen[a.Issuer] = true
			issuers = append(issuers, a.Issuer)
		}
	}
	out, err := r.Store.HomeDomains(ctx, issuers)
	if err != nil {
		return nil, err
	}
	if r.Lookup == nil {
		return out, nil
	}
	var missing []string
	for _, i := range issuers {
		if out[i] == "" {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	more, err := r.Lookup(ctx, missing)
	for k, v := range more {
		out[k] = v
	}
	return out, err
}

func (r *Resolver) resolve(ctx context.Context, a Asset, domains map[string]string, lookupErr error, tomls map[string]tomlResult) Result {
	res := Result{Key: a.Key}
	domain := strings.ToLower(strings.TrimSpace(domains[a.Issuer]))
	if domain == "" {
		if lookupErr != nil {
			return fail(res, StatusError, "home_domain lookup: "+lookupErr.Error())
		}
		return fail(res, StatusNone, "issuer has no home_domain")
	}
	res.HomeDomain = domain
	if !govalidator.IsDNSName(domain) || !strings.Contains(domain, ".") {
		return fail(res, StatusNone, "home_domain is not a DNS name")
	}
	t, ok := tomls[domain]
	if !ok {
		resp, err := r.Toml.GetStellarToml(domain)
		t = tomlResult{resp, err}
		tomls[domain] = t
	}
	if t.err != nil {
		return fail(res, StatusError, "stellar.toml: "+t.err.Error())
	}
	img := imageFor(t.resp, a.Code, a.Issuer)
	if img == "" {
		return fail(res, StatusNone, "stellar.toml lists no image for this code and issuer")
	}
	check := r.checkURL
	if check == nil {
		check = checkImageURL
	}
	if err := check(img); err != nil {
		return fail(res, StatusNone, err.Error())
	}
	res.ImageURL = img
	data, ct, err := fetchImage(ctx, r.HTTP, img)
	if err != nil {
		var bad *badImage
		if errors.As(err, &bad) {
			return fail(res, StatusNone, err.Error())
		}
		return fail(res, StatusError, err.Error())
	}
	sum := sha256.Sum256(data)
	res.Status, res.Data, res.ContentType, res.SHA256 = StatusOK, data, ct, hex.EncodeToString(sum[:])
	return res
}

func fail(r Result, status, detail string) Result {
	if len(detail) > 300 {
		detail = detail[:300]
	}
	r.Status, r.Detail = status, detail
	return r
}

// imageFor returns the image of the CURRENCIES entry for exactly code+issuer.
func imageFor(t *stellartoml.Response, code, issuer string) string {
	if t == nil {
		return ""
	}
	for _, c := range t.Currencies {
		if c.Code == code && c.Issuer == issuer {
			return strings.TrimSpace(c.Image)
		}
	}
	return ""
}

// checkImageURL accepts only https URLs on a DNS host with the default port.
func checkImageURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return fmt.Errorf("image URL must be plain https: %q", trim(raw))
	}
	h := u.Hostname()
	if !govalidator.IsDNSName(h) || !strings.Contains(h, ".") {
		return fmt.Errorf("image host is not a DNS name: %q", trim(h))
	}
	return nil
}

func trim(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
