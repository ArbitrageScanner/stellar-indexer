package verify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	rpc "github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// A fake RPC answering getLedgerEntries with one wasm instance; the other
// requested contract is absent (archived) and must come back as "missing".
func TestRPCResolver(t *testing.T) {
	var hash xdr.Hash
	hash[0] = 0xab
	var cid xdr.ContractId
	cid[0] = 1
	known, _ := strkey.Encode(strkey.VersionByteContract, cid[:])
	unknown, _ := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any `json:"id"`
			Params struct {
				Keys []string `json:"keys"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		knownKey, _ := instanceKey(known)
		var entries []map[string]any
		for _, k := range req.Params.Keys {
			if k != knownKey {
				continue
			}
			data := xdr.LedgerEntryData{Type: xdr.LedgerEntryTypeContractData, ContractData: &xdr.ContractDataEntry{
				Contract:   xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &cid},
				Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
				Durability: xdr.ContractDataDurabilityPersistent,
				Val: xdr.ScVal{Type: xdr.ScValTypeScvContractInstance, Instance: &xdr.ScContractInstance{
					Executable: xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableWasm, WasmHash: &hash}}},
			}}
			b, _ := data.MarshalBinary()
			entries = append(entries, map[string]any{"key": k, "xdr": base64.StdEncoding.EncodeToString(b), "lastModifiedLedgerSeq": 7})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"entries": entries, "latestLedger": 9}})
	}))
	defer srv.Close()

	codes, err := RPCResolver{Client: rpc.NewClient(srv.URL, srv.Client())}.Resolve(context.Background(), []string{known, unknown, "not-a-strkey"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range codes {
		got[c.ContractID] = c.Executable + ":" + c.WasmHash
	}
	if got[known] != "wasm:ab00000000000000000000000000000000000000000000000000000000000000" || got[unknown] != "missing:" || got["not-a-strkey"] != "missing:" {
		t.Fatalf("resolved %v", got)
	}
}
