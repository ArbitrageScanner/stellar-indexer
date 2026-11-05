package processor

import (
	"encoding/hex"
	"sync"

	assetProto "github.com/stellar/go-stellar-sdk/asset"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const NativeKey = "native"

// AssetKey returns "native" or "CODE:ISSUER".
func AssetKey(a xdr.Asset) string {
	if a.Type == xdr.AssetTypeAssetTypeNative {
		return NativeKey
	}
	return a.StringCanonical()
}

func protoAssetKey(a *assetProto.Asset) string {
	if a == nil {
		return ""
	}
	if a.GetNative() {
		return NativeKey
	}
	if ia := a.GetIssuedAsset(); ia != nil {
		return ia.AssetCode + ":" + ia.Issuer
	}
	return ""
}

func trustLineAssetKey(a xdr.TrustLineAsset) string {
	if a.Type == xdr.AssetTypeAssetTypePoolShare {
		return "pool:" + hex.EncodeToString(a.LiquidityPoolId[:])
	}
	return AssetKey(a.ToAsset())
}

// Registry maps Stellar Asset Contract addresses to classic asset keys so that
// Soroban swaps and classic trades price the same asset under one key.
type Registry struct {
	passphrase string
	mu         sync.RWMutex
	byContract map[string]string // contract strkey -> asset key
	known      map[string]bool   // asset keys already registered
}

func NewRegistry(passphrase string) *Registry {
	return &Registry{passphrase: passphrase, byContract: map[string]string{}, known: map[string]bool{}}
}

// Preload adds previously persisted mappings.
func (r *Registry) Preload(assetKey, contractID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.known[assetKey] = true
	if contractID != "" && contractID != assetKey {
		r.byContract[contractID] = assetKey
	}
}

// Classic registers a classic asset and returns its row if it is new.
func (r *Registry) Classic(a xdr.Asset, ledger uint32) (string, *Asset) {
	key := AssetKey(a)
	r.mu.RLock()
	seen := r.known[key]
	r.mu.RUnlock()
	if seen {
		return key, nil
	}
	row := &Asset{Key: key, FirstSeen: ledger}
	switch a.Type {
	case xdr.AssetTypeAssetTypeNative:
		row.Type = "native"
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		row.Type = "credit_alphanum4"
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		row.Type = "credit_alphanum12"
	}
	if row.Type != "native" {
		var t, code, issuer string
		_ = a.Extract(&t, &code, &issuer)
		row.Code, row.Issuer = code, issuer
	} else {
		row.Code = "XLM"
	}
	if id, err := a.ContractID(r.passphrase); err == nil {
		row.ContractID = contractStrkey(id)
	}
	r.mu.Lock()
	r.known[key] = true
	if row.ContractID != "" {
		r.byContract[row.ContractID] = key
	}
	r.mu.Unlock()
	return key, row
}

// Token resolves a token contract address to an asset key, registering
// unknown contracts as custom tokens (returned row is non-nil when new).
func (r *Registry) Token(contractID string, ledger uint32) (string, *Asset) {
	r.mu.RLock()
	key, ok := r.byContract[contractID]
	seen := r.known[contractID]
	r.mu.RUnlock()
	if ok {
		return key, nil
	}
	if seen {
		return contractID, nil
	}
	r.mu.Lock()
	r.known[contractID] = true
	r.mu.Unlock()
	return contractID, &Asset{Key: contractID, Type: "contract", ContractID: contractID, FirstSeen: ledger}
}

// Lookup resolves without registering.
func (r *Registry) Lookup(contractID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if key, ok := r.byContract[contractID]; ok {
		return key
	}
	return contractID
}

// Forget undoes registration of assets whose batch failed to persist, so they
// are emitted again when the ledger is retried.
func (r *Registry) Forget(rows []Asset) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range rows {
		delete(r.known, a.Key)
		if a.ContractID != "" && r.byContract[a.ContractID] == a.Key {
			delete(r.byContract, a.ContractID)
		}
	}
}
