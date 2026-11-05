package processor

import (
	"encoding/hex"
	"math/big"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ScValToJSON converts an ScVal into a plain JSON-friendly value. Integers wider
// than 53 bits are returned as decimal strings so nothing is lost in JSON.
// Maps whose keys are all symbols/strings become objects; other maps become
// arrays of [key, value] pairs.
func ScValToJSON(v xdr.ScVal) any {
	switch v.Type {
	case xdr.ScValTypeScvBool:
		return *v.B
	case xdr.ScValTypeScvVoid:
		return nil
	case xdr.ScValTypeScvError:
		return map[string]any{"error": v.Error.Type.String()}
	case xdr.ScValTypeScvU32:
		return uint32(*v.U32)
	case xdr.ScValTypeScvI32:
		return int32(*v.I32)
	case xdr.ScValTypeScvU64:
		return bigString(new(big.Int).SetUint64(uint64(*v.U64)))
	case xdr.ScValTypeScvI64:
		return bigString(big.NewInt(int64(*v.I64)))
	case xdr.ScValTypeScvTimepoint:
		return uint64(*v.Timepoint)
	case xdr.ScValTypeScvDuration:
		return uint64(*v.Duration)
	case xdr.ScValTypeScvU128, xdr.ScValTypeScvI128, xdr.ScValTypeScvU256, xdr.ScValTypeScvI256:
		n, _ := ScValBigInt(v)
		return bigString(n)
	case xdr.ScValTypeScvBytes:
		return hex.EncodeToString(*v.Bytes)
	case xdr.ScValTypeScvString:
		return string(*v.Str)
	case xdr.ScValTypeScvSymbol:
		return string(*v.Sym)
	case xdr.ScValTypeScvVec:
		if v.Vec == nil || *v.Vec == nil {
			return []any{}
		}
		out := make([]any, 0, len(**v.Vec))
		for _, e := range **v.Vec {
			out = append(out, ScValToJSON(e))
		}
		return out
	case xdr.ScValTypeScvMap:
		if v.Map == nil || *v.Map == nil {
			return map[string]any{}
		}
		m := **v.Map
		obj := make(map[string]any, len(m))
		for _, e := range m {
			k, ok := scKeyString(e.Key)
			if !ok {
				pairs := make([]any, 0, len(m))
				for _, e := range m {
					pairs = append(pairs, []any{ScValToJSON(e.Key), ScValToJSON(e.Val)})
				}
				return pairs
			}
			obj[k] = ScValToJSON(e.Val)
		}
		return obj
	case xdr.ScValTypeScvAddress:
		s, _ := ScAddressString(*v.Address)
		return s
	case xdr.ScValTypeScvLedgerKeyContractInstance:
		return "<contract_instance>"
	case xdr.ScValTypeScvLedgerKeyNonce:
		return map[string]any{"nonce": int64(v.NonceKey.Nonce)}
	case xdr.ScValTypeScvContractInstance:
		return "<instance>"
	}
	return v.Type.String()
}

func bigString(n *big.Int) any {
	if n == nil {
		return nil
	}
	return n.String()
}

func scKeyString(v xdr.ScVal) (string, bool) {
	switch v.Type {
	case xdr.ScValTypeScvSymbol:
		return string(*v.Sym), true
	case xdr.ScValTypeScvString:
		return string(*v.Str), true
	}
	return "", false
}

// ScValSymbol returns the symbol or string content of v.
func ScValSymbol(v xdr.ScVal) (string, bool) { return scKeyString(v) }

// ScValBigInt returns integer ScVals of any width as a big.Int.
func ScValBigInt(v xdr.ScVal) (*big.Int, bool) {
	switch v.Type {
	case xdr.ScValTypeScvU32:
		return big.NewInt(int64(*v.U32)), true
	case xdr.ScValTypeScvI32:
		return big.NewInt(int64(*v.I32)), true
	case xdr.ScValTypeScvU64:
		return new(big.Int).SetUint64(uint64(*v.U64)), true
	case xdr.ScValTypeScvI64:
		return big.NewInt(int64(*v.I64)), true
	case xdr.ScValTypeScvU128:
		return words(false, uint64(v.U128.Hi), uint64(v.U128.Lo)), true
	case xdr.ScValTypeScvI128:
		return words(true, uint64(v.I128.Hi), uint64(v.I128.Lo)), true
	case xdr.ScValTypeScvU256:
		p := v.U256
		return words(false, uint64(p.HiHi), uint64(p.HiLo), uint64(p.LoHi), uint64(p.LoLo)), true
	case xdr.ScValTypeScvI256:
		p := v.I256
		return words(true, uint64(p.HiHi), uint64(p.HiLo), uint64(p.LoHi), uint64(p.LoLo)), true
	}
	return nil, false
}

// words builds a two's-complement integer from big-endian 64-bit words.
func words(signed bool, ws ...uint64) *big.Int {
	n := new(big.Int)
	for _, w := range ws {
		n.Lsh(n, 64)
		n.Or(n, new(big.Int).SetUint64(w))
	}
	bits := uint(64 * len(ws))
	if signed && n.Bit(int(bits-1)) == 1 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), bits))
	}
	return n
}

// ScAddressString renders any ScAddress arm as its strkey.
func ScAddressString(a xdr.ScAddress) (string, error) {
	return a.String()
}

// ScValAddress returns the strkey if v is an address.
func ScValAddress(v xdr.ScVal) (string, bool) {
	if v.Type != xdr.ScValTypeScvAddress || v.Address == nil {
		return "", false
	}
	s, err := v.Address.String()
	return s, err == nil
}

func contractStrkey(id [32]byte) string {
	s, _ := strkey.Encode(strkey.VersionByteContract, id[:])
	return s
}

// scVec returns the elements if v is a vec.
func scVec(v xdr.ScVal) ([]xdr.ScVal, bool) {
	if v.Type != xdr.ScValTypeScvVec || v.Vec == nil || *v.Vec == nil {
		return nil, false
	}
	return **v.Vec, true
}

// scMap returns symbol-keyed map fields.
func scMap(v xdr.ScVal) (map[string]xdr.ScVal, bool) {
	if v.Type != xdr.ScValTypeScvMap || v.Map == nil || *v.Map == nil {
		return nil, false
	}
	out := map[string]xdr.ScVal{}
	for _, e := range **v.Map {
		if k, ok := scKeyString(e.Key); ok {
			out[k] = e.Val
		}
	}
	return out, true
}
