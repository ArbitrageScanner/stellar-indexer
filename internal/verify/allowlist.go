package verify

import (
	"encoding/json"
	"fmt"
	"os"
)

// Default allowlists, observed on 2026-11-05 by resolving the code of every
// contract with protocol-shaped events (`indexer contract-code`). A hash is
// listed when it is shared by many factory-deployed pools of the protocol, or
// it is the protocol's published router. One-off contracts imitating an event
// shape (including every "generic AMM") are deliberately absent: they stay
// unverified until an operator reviews and allowlists them.
// Protocols upgrade their wasm; extend these with PROTOCOL_ALLOWLIST_FILE.
var defaults = map[string]Allowlist{
	"pubnet": {
		"aquarius": {
			"155a17b9929ffb1f9e84bd6ef5c00a4d613c1ab5f4ad4c502d84515250cc2907", // pools (35 contracts)
			"d691135aade93ff0f7c229e009cde042130a05124cf7202b03d11246b4f9b473", // pools (87)
			"22dff7242d2bc0ea4a4727b4b2cac33b188304d5945740ad24d8a33a5d22741e", // pools (17)
			"c99539b023df4a28d7857d33ca4c915c2e20fec04f12b125132b84fbdf94dad3", // router CBQDHNBF…6QUK
		},
		"soroswap": {
			"18051456816b66f12e773a56f77c5794fac1b1fb7ab6e22d4fad5a412770f73e", // pairs (25)
			"4c3db3ebd2d6a2ab23de1f622eaabb39501539b4611b68622ec4e47f76c4ba07", // router CAG5LRYQ…JDDH
		},
		"blend": {"a41fc53d6753b6c04eb15b021c55052366a4c8e0e21bc72700f461264ec1350e"}, // lending pools
		"clmm":  {"003710b383f9da7d650a7f719a7be479110266427817ebbed61d924505fcd7c7"}, // pools (17)
	},
	"testnet": {
		"aquarius": {
			"d691135aade93ff0f7c229e009cde042130a05124cf7202b03d11246b4f9b473",
			"155a17b9929ffb1f9e84bd6ef5c00a4d613c1ab5f4ad4c502d84515250cc2907",
			"22dff7242d2bc0ea4a4727b4b2cac33b188304d5945740ad24d8a33a5d22741e",
		},
		"soroswap": {
			"8447525edd62f72ffaf52136358034657ea0511a8fec1cd0ebde649f86cca464", // pairs (15)
			"4b95bbf9caec2c6e00c786f53c5f392c2fcdb8435ac0a862ab5e0645eb65824c", // router CCJUD55A…7BRD
		},
		"blend": {"a41fc53d6753b6c04eb15b021c55052366a4c8e0e21bc72700f461264ec1350e"},
		"clmm":  {"003710b383f9da7d650a7f719a7be479110266427817ebbed61d924505fcd7c7"},
	},
}

// LoadAllowlist returns the built-in list for network, extended (or, with
// "replace": true, replaced) by a JSON file {"replace": bool, "protocols": {"name": ["hash", ...]}}.
func LoadAllowlist(network, file string) (Allowlist, error) {
	out := Allowlist{}
	for p, hs := range defaults[network] {
		out[p] = append([]string(nil), hs...)
	}
	if file == "" {
		return out, nil
	}
	b, err := os.ReadFile(file) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, err
	}
	var f struct {
		Replace   bool                `json:"replace"`
		Protocols map[string][]string `json:"protocols"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if f.Replace {
		out = Allowlist{}
	}
	for p, hs := range f.Protocols {
		for _, h := range hs {
			if len(h) != 64 {
				return nil, fmt.Errorf("%s: protocol %s: %q is not a 32-byte hex wasm hash", file, p, h)
			}
			out[p] = append(out[p], h)
		}
	}
	return out, nil
}
