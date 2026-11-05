package processor

import (
	"math/big"
	"sync"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Protocol event shapes were taken from live pubnet events (Nov 2026):
//
//	Soroswap pair:   topics ["SoroswapPair"(string), action]   swap data {amount_0_in, amount_1_in, amount_0_out, amount_1_out, to}
//	Soroswap router: topics ["SoroswapRouter"(string), action] swap data {amounts, path, to}
//	Aquarius pool:   topics ["trade", token_in, token_out, user]  data [amount_in, amount_out, fee]
//	Aquarius router: topics [action, [tokens...], user]
//	Blend pool:      topics [action, asset, user]  data [amount, b/d_tokens]
//	CLMM pool:       topics ["swap"]  data {amount0, amount1, sqrt_price_x96, liquidity, tick, sender, recipient}
//	Generic AMM:     topics ["swap", ...]  data {amount_in, amount_out, ...}

type swapCandidate struct {
	pool, venue, user   string
	tokenIn, tokenOut   string // contract addresses when the event names them
	amountIn, amountOut *big.Int
	eventIndex          uint32
}

type decoded struct {
	protocol string
	event    *ProtocolEvent
	swap     *swapCandidate
}

var blendActions = map[string]bool{
	"supply": true, "withdraw": true, "supply_collateral": true, "withdraw_collateral": true,
	"borrow": true, "repay": true,
}

// protocolIndex remembers contracts already attributed to a protocol so that
// their less distinctive events (auctions, admin calls) are labelled too.
type protocolIndex struct {
	mu         sync.RWMutex
	byContract map[string]string
}

func newProtocolIndex() *protocolIndex { return &protocolIndex{byContract: map[string]string{}} }

func (pi *protocolIndex) get(c string) string {
	pi.mu.RLock()
	defer pi.mu.RUnlock()
	return pi.byContract[c]
}

func (pi *protocolIndex) set(c, p string) {
	pi.mu.Lock()
	pi.byContract[c] = p
	pi.mu.Unlock()
}

func (pi *protocolIndex) decode(contract string, topics []xdr.ScVal, data xdr.ScVal) decoded {
	if len(topics) == 0 {
		return decoded{}
	}
	t0, _ := ScValSymbol(topics[0])
	isString := topics[0].Type == xdr.ScValTypeScvString

	// Soroswap: string namespace topic followed by an action symbol.
	if isString && len(topics) >= 2 {
		action, _ := ScValSymbol(topics[1])
		switch t0 {
		case "SoroswapPair":
			pi.set(contract, "soroswap")
			return decodeSoroswapPair(action, data)
		case "SoroswapRouter", "SoroswapFactory", "SoroswapAggregator":
			pi.set(contract, "soroswap")
			return decoded{protocol: "soroswap", event: genericEvent(t0[8:]+"."+action, topics, data)}
		}
	}

	// Aquarius pool trade: ("trade", token_in, token_out, user) -> [in, out, fee]
	if t0 == "trade" && len(topics) == 4 {
		tin, ok1 := ScValAddress(topics[1])
		tout, ok2 := ScValAddress(topics[2])
		user, _ := ScValAddress(topics[3])
		if v, ok := scVec(data); ok1 && ok2 && ok && len(v) >= 2 {
			in, _ := ScValBigInt(v[0])
			out, _ := ScValBigInt(v[1])
			pi.set(contract, "aquarius")
			ev := &ProtocolEvent{Action: "trade", User: user, Asset: tin, Amount0: str(in), Amount1: str(out), Data: ScValToJSON(data)}
			return decoded{protocol: "aquarius", event: ev,
				swap: &swapCandidate{user: user, tokenIn: tin, tokenOut: tout, amountIn: in, amountOut: out}}
		}
	}

	// Aquarius router: (action, [tokens], user)
	if len(topics) == 3 && (t0 == "swap" || t0 == "deposit" || t0 == "withdraw") {
		if toks, ok := scVec(topics[1]); ok && len(toks) >= 2 {
			if _, ok := ScValAddress(toks[0]); ok {
				pi.set(contract, "aquarius")
				user, _ := ScValAddress(topics[2])
				return decoded{protocol: "aquarius", event: &ProtocolEvent{Action: "router_" + t0, User: user, Data: ScValToJSON(data)}}
			}
		}
	}

	// Blend lending pool: (action, asset, user) -> [amount, tokens]
	// flash_loan carries the receiver contract too: (flash_loan, asset, receiver, user).
	if (blendActions[t0] && len(topics) == 3) || (t0 == "flash_loan" && len(topics) == 4) {
		asset, ok1 := ScValAddress(topics[1])
		user, ok2 := ScValAddress(topics[len(topics)-1])
		if v, ok := scVec(data); ok1 && ok2 && ok && len(v) == 2 {
			a0, okA := ScValBigInt(v[0])
			a1, okB := ScValBigInt(v[1])
			if okA && okB {
				pi.set(contract, "blend")
				return decoded{protocol: "blend", event: &ProtocolEvent{Action: t0, User: user, Asset: asset, Amount0: a0.String(), Amount1: a1.String()}}
			}
		}
	}

	if t0 == "swap" {
		if m, ok := scMap(data); ok {
			// Concentrated liquidity (Uniswap v3 style): signed amount0/amount1.
			if a0v, ok := m["amount0"]; ok {
				if a1v, ok := m["amount1"]; ok {
					a0, _ := ScValBigInt(a0v)
					a1, _ := ScValBigInt(a1v)
					user, _ := ScValAddress(m["recipient"])
					pi.set(contract, "clmm")
					ev := &ProtocolEvent{Action: "swap", User: user, Amount0: str(a0), Amount1: str(a1), Data: ScValToJSON(data)}
					d := decoded{protocol: "clmm", event: ev}
					if a0 != nil && a1 != nil {
						// positive = paid into the pool
						if a0.Sign() > 0 && a1.Sign() < 0 {
							d.swap = &swapCandidate{user: user, amountIn: a0, amountOut: new(big.Int).Neg(a1)}
						} else if a1.Sign() > 0 && a0.Sign() < 0 {
							d.swap = &swapCandidate{user: user, amountIn: a1, amountOut: new(big.Int).Neg(a0)}
						}
					}
					return d
				}
			}
			if inv, ok := m["amount_in"]; ok {
				if outv, ok := m["amount_out"]; ok {
					in, _ := ScValBigInt(inv)
					out, _ := ScValBigInt(outv)
					user, _ := ScValAddress(m["recipient"])
					if user == "" {
						user, _ = ScValAddress(m["to"])
					}
					p := pi.get(contract)
					if p == "" {
						p = "amm_generic"
					}
					return decoded{protocol: p,
						event: &ProtocolEvent{Action: "swap", User: user, Amount0: str(in), Amount1: str(out), Data: ScValToJSON(data)},
						swap:  &swapCandidate{user: user, amountIn: in, amountOut: out}}
				}
			}
		}
	}

	// Anything else from a contract we already attributed.
	if p := pi.get(contract); p != "" && t0 != "" {
		if isTokenEvent(t0, topics) {
			return decoded{}
		}
		return decoded{protocol: p, event: genericEvent(t0, topics, data)}
	}
	return decoded{}
}

func decodeSoroswapPair(action string, data xdr.ScVal) decoded {
	d := decoded{protocol: "soroswap"}
	m, _ := scMap(data)
	ev := &ProtocolEvent{Action: action, Data: ScValToJSON(data)}
	d.event = ev
	get := func(k string) *big.Int {
		if v, ok := m[k]; ok {
			n, _ := ScValBigInt(v)
			return n
		}
		return nil
	}
	switch action {
	case "swap":
		in0, in1, out0, out1 := get("amount_0_in"), get("amount_1_in"), get("amount_0_out"), get("amount_1_out")
		ev.User, _ = ScValAddress(m["to"])
		s := &swapCandidate{user: ev.User}
		if in0 != nil && in0.Sign() > 0 {
			s.amountIn, s.amountOut = in0, out1
		} else {
			s.amountIn, s.amountOut = in1, out0
		}
		ev.Amount0, ev.Amount1 = str(s.amountIn), str(s.amountOut)
		d.swap = s
	case "sync":
		ev.Amount0, ev.Amount1 = str(get("new_reserve_0")), str(get("new_reserve_1"))
	case "deposit", "withdraw":
		ev.User, _ = ScValAddress(m["to"])
		ev.Amount0, ev.Amount1 = str(get("amount_0")), str(get("amount_1"))
	}
	return d
}

func genericEvent(action string, topics []xdr.ScVal, data xdr.ScVal) *ProtocolEvent {
	ev := &ProtocolEvent{Action: action, Data: ScValToJSON(data)}
	for _, t := range topics[1:] {
		if a, ok := ScValAddress(t); ok {
			if ev.User == "" {
				ev.User = a
			}
		}
	}
	return ev
}

func str(n *big.Int) string {
	if n == nil {
		return ""
	}
	return n.String()
}
