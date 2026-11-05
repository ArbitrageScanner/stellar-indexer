package verify

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	rpc "github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
)

// RPCResolver reads contract instance entries with getLedgerEntries.
type RPCResolver struct{ Client *rpc.Client }

const maxKeysPerCall = 100

func (r RPCResolver) Resolve(ctx context.Context, ids []string) ([]processor.ContractCode, error) {
	var out []processor.ContractCode
	for len(ids) > 0 {
		n := min(len(ids), maxKeysPerCall)
		chunk := ids[:n]
		ids = ids[n:]
		keys := make([]string, 0, len(chunk))
		byKey := map[string]string{}
		for _, id := range chunk {
			k, err := instanceKey(id)
			if err != nil {
				out = append(out, processor.ContractCode{ContractID: id, Executable: "missing"})
				continue
			}
			keys = append(keys, k)
			byKey[k] = id
		}
		if len(keys) == 0 {
			continue
		}
		resp, err := r.Client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
		if err != nil {
			return nil, err
		}
		found := map[string]bool{}
		for _, e := range resp.Entries {
			id := byKey[e.KeyXDR]
			var data xdr.LedgerEntryData
			if err := xdr.SafeUnmarshalBase64(e.DataXDR, &data); err != nil {
				return nil, fmt.Errorf("decode entry for %s: %w", id, err)
			}
			c := codeOf(id, data)
			found[c.ContractID] = true
			out = append(out, c)
		}
		// Not returned: archived or never existed. Record it so we do not ask again.
		for _, id := range chunk {
			if !found[id] {
				out = append(out, processor.ContractCode{ContractID: id, Executable: "missing"})
			}
		}
	}
	return out, nil
}

func instanceKey(id string) (string, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, id)
	if err != nil {
		return "", err
	}
	var cid xdr.ContractId
	copy(cid[:], raw)
	key := xdr.LedgerKey{Type: xdr.LedgerEntryTypeContractData, ContractData: &xdr.LedgerKeyContractData{
		Contract:   xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &cid},
		Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
		Durability: xdr.ContractDataDurabilityPersistent,
	}}
	b, err := key.MarshalBinary()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func codeOf(id string, d xdr.LedgerEntryData) processor.ContractCode {
	c := processor.ContractCode{ContractID: id, Executable: "missing"}
	cd, ok := d.GetContractData()
	if !ok {
		return c
	}
	inst, ok := cd.Val.GetInstance()
	if !ok {
		return c
	}
	switch inst.Executable.Type {
	case xdr.ContractExecutableTypeContractExecutableWasm:
		c.Executable, c.WasmHash = "wasm", hex.EncodeToString(inst.Executable.WasmHash[:])
	case xdr.ContractExecutableTypeContractExecutableStellarAsset:
		c.Executable = "stellar_asset"
	}
	return c
}
