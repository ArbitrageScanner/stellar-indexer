// Package verify gates protocol attribution on contract code.
//
// Protocol events are recognised by their shape (topics and data layout),
// which anyone can imitate: a contract emitting ("SoroswapPair", "swap")
// events would otherwise inject fake fills into candles and prices. The
// verifier resolves each pool contract's executable (wasm hash) from ledger
// changes or, for contracts deployed before indexing began, from RPC, and
// marks events and fills verified only when the hash is allowlisted for that
// protocol. Unverified data is still stored, flagged, and excluded from
// prices.
package verify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
)

// Allowlist maps a protocol name to the wasm hashes (hex) of its contracts.
type Allowlist map[string][]string

// Resolver fetches the current executable of contracts (e.g. via RPC).
type Resolver interface {
	Resolve(ctx context.Context, ids []string) ([]processor.ContractCode, error)
}

type Verifier struct {
	enforce bool
	allow   map[string]map[string]bool
	res     Resolver
	log     *slog.Logger

	mu     sync.Mutex
	codes  map[string]processor.ContractCode
	warned map[string]bool
}

// New builds a verifier. With enforce=false every protocol event is accepted
// (the pre-verification behaviour); known holds cached codes from the DB.
func New(enforce bool, allow Allowlist, res Resolver, known []processor.ContractCode, log *slog.Logger) *Verifier {
	v := &Verifier{enforce: enforce, allow: map[string]map[string]bool{}, res: res, log: log,
		codes: map[string]processor.ContractCode{}, warned: map[string]bool{}}
	for p, hs := range allow {
		v.allow[p] = map[string]bool{}
		for _, h := range hs {
			v.allow[p][h] = true
		}
	}
	for _, c := range known {
		v.codes[c.ContractID] = c
	}
	return v
}

// Allowed reports whether contract code is allowlisted for protocol.
func (v *Verifier) Allowed(protocol string, c processor.ContractCode) bool {
	if !v.enforce {
		return true
	}
	return c.Executable == "wasm" && v.allow[protocol][c.WasmHash]
}

// Annotate sets Verified on the batches' protocol events, contract events and
// Soroban fills. Codes changed in the batches are applied first; unknown
// contracts are resolved in one round trip and appended to the last batch so
// they are persisted with it. A resolver error aborts (the caller retries) so
// a transient RPC failure never stores genuine data as unverified.
func (v *Verifier) Annotate(ctx context.Context, batches []*processor.Batch) error {
	if len(batches) == 0 {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, b := range batches {
		for _, c := range b.Codes {
			if old, ok := v.codes[c.ContractID]; !ok || old.Ledger <= c.Ledger {
				v.codes[c.ContractID] = c
			}
		}
	}
	if v.enforce {
		var missing []string
		seen := map[string]bool{}
		need := func(id string) {
			if _, ok := v.codes[id]; !ok && !seen[id] && id != "" {
				seen[id] = true
				missing = append(missing, id)
			}
		}
		for _, b := range batches {
			for _, e := range b.ProtocolEvents {
				need(e.ContractID)
			}
			for _, t := range b.Trades {
				if !t.Verified {
					need(t.Pool)
				}
			}
		}
		if len(missing) > 0 && v.res != nil {
			got, err := v.res.Resolve(ctx, missing)
			if err != nil {
				metrics.CodeLookups.WithLabelValues("error").Add(float64(len(missing)))
				return fmt.Errorf("resolve contract code: %w", err)
			}
			metrics.CodeLookups.WithLabelValues("ok").Add(float64(len(got)))
			last := batches[len(batches)-1]
			for _, c := range got {
				v.codes[c.ContractID] = c
				last.Codes = append(last.Codes, c)
			}
		}
	}
	for _, b := range batches {
		for i := range b.ProtocolEvents {
			e := &b.ProtocolEvents[i]
			e.Verified = v.Allowed(e.Protocol, v.codes[e.ContractID])
			if e.Verified {
				metrics.VerifiedEvents.WithLabelValues(e.Protocol).Inc()
			} else {
				metrics.UnverifiedEvents.WithLabelValues(e.Protocol).Inc()
				v.warnOnce(e.Protocol, e.ContractID)
			}
		}
		for i := range b.ContractEvents {
			e := &b.ContractEvents[i]
			if e.Protocol != "" {
				e.Verified = v.Allowed(e.Protocol, v.codes[e.ContractID])
			}
		}
		for i := range b.Trades {
			t := &b.Trades[i]
			if t.Verified { // classic venues
				continue
			}
			t.Verified = v.Allowed(t.Venue, v.codes[t.Pool])
		}
	}
	return nil
}

func (v *Verifier) warnOnce(protocol, contract string) {
	if !v.enforce || v.warned[contract] || v.log == nil {
		return
	}
	v.warned[contract] = true
	c := v.codes[contract]
	v.log.Warn("protocol-shaped events from non-allowlisted code; stored as unverified",
		"protocol", protocol, "contract", contract, "executable", c.Executable, "wasm_hash", c.WasmHash)
}
