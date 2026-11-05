// Package processor turns a LedgerCloseMeta into analytics rows. It is pure
// (no I/O) so it can be tested against recorded ledgers.
package processor

import (
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/processors/token_transfer"
	"github.com/stellar/go-stellar-sdk/toid"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type Options struct {
	Operations  bool
	State       bool
	FeeEvents   bool
	TokenEvents bool // keep standard token events (transfer/mint/...) in contract_events too
	SkipFailed  bool // drop failed transactions/operations (still counted in ledger stats)
}

type Processor struct {
	passphrase string
	opts       Options
	reg        *Registry
	ttp        *token_transfer.EventsProcessor
	protocols  *protocolIndex
}

func New(passphrase string, reg *Registry, opts Options) *Processor {
	return &Processor{
		passphrase: passphrase,
		opts:       opts,
		reg:        reg,
		ttp:        token_transfer.NewEventsProcessor(passphrase),
		protocols:  newProtocolIndex(),
	}
}

func (p *Processor) Registry() *Registry { return p.reg }

// Result wraps a batch with non-fatal decode warnings.
type Result struct {
	Batch    *Batch
	Warnings []string
}

func (p *Processor) Process(lcm xdr.LedgerCloseMeta) (*Result, error) {
	hdr := lcm.LedgerHeaderHistoryEntry()
	seq := lcm.LedgerSequence()
	closedAt := lcm.ClosedAt().UTC()
	b := &Batch{Ledger: Ledger{
		Sequence:    seq,
		Hash:        hex.EncodeToString(hdr.Hash[:]),
		PrevHash:    hex.EncodeToString(hdr.Header.PreviousLedgerHash[:]),
		ClosedAt:    closedAt,
		Protocol:    uint32(hdr.Header.LedgerVersion),
		BaseFee:     uint32(hdr.Header.BaseFee),
		BaseReserve: uint32(hdr.Header.BaseReserve),
		TotalCoins:  int64(hdr.Header.TotalCoins),
		FeePool:     int64(hdr.Header.FeePool),
	}}
	res := &Result{Batch: b}
	ctx := &ledgerCtx{p: p, b: b, res: res, seq: seq, closedAt: closedAt}

	reader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(p.passphrase, lcm)
	if err != nil {
		return nil, fmt.Errorf("ledger %d: tx reader: %w", seq, err)
	}
	defer reader.Close()
	for {
		tx, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ledger %d: read tx: %w", seq, err)
		}
		if err := ctx.transaction(tx); err != nil {
			return nil, fmt.Errorf("ledger %d tx %s: %w", seq, tx.Hash.HexString(), err)
		}
	}

	if p.opts.State {
		if err := ctx.state(lcm); err != nil {
			return nil, fmt.Errorf("ledger %d: state: %w", seq, err)
		}
	}
	return res, nil
}

type ledgerCtx struct {
	p        *Processor
	b        *Batch
	res      *Result
	seq      uint32
	closedAt time.Time
}

func (c *ledgerCtx) warn(format string, args ...any) {
	c.res.Warnings = append(c.res.Warnings, fmt.Sprintf("ledger %d: ", c.seq)+fmt.Sprintf(format, args...))
}

func (c *ledgerCtx) asset(a xdr.Asset) string {
	key, row := c.p.reg.Classic(a, c.seq)
	if row != nil {
		c.b.Assets = append(c.b.Assets, *row)
	}
	return key
}

func (c *ledgerCtx) token(contractID string) string {
	key, row := c.p.reg.Token(contractID, c.seq)
	if row != nil {
		c.b.Assets = append(c.b.Assets, *row)
	}
	return key
}

func (c *ledgerCtx) transaction(tx ingest.LedgerTransaction) error {
	b := c.b
	hash := tx.Hash.HexString()
	ok := tx.Successful()
	src, _ := tx.Account()
	feeAcct, _ := tx.FeeAccountAddress()
	fee, _ := tx.FeeCharged()
	t := Transaction{
		Hash:       hash,
		Ledger:     c.seq,
		Index:      tx.Index,
		ID:         tx.ID(),
		ClosedAt:   c.closedAt,
		Source:     src,
		FeeAccount: feeAcct,
		SeqNum:     tx.AccountSequence(),
		FeeCharged: fee,
		MaxFee:     int64(tx.MaxFee()),
		OpCount:    tx.OperationCount(),
		Successful: ok,
		ResultCode: tx.ResultCode(),
		MemoType:   tx.MemoType(),
		Memo:       tx.Memo(),
		IsSoroban:  tx.IsSorobanTx(),
	}
	if nmf, ok := tx.NewMaxFee(); ok {
		t.MaxFee = nmf
	}
	if inner, ok := tx.InnerTransactionHash(); ok {
		t.InnerHash = inner
	}
	if t.IsSoroban {
		b.Ledger.SorobanTxCount++
		if rf, ok := tx.SorobanResourceFee(); ok {
			t.ResourceFee = rf
		}
		if ins, ok := tx.SorobanResourcesInstructions(); ok {
			t.Instructions = ins
		}
	}
	b.Ledger.TxCount++
	b.Ledger.FeesCharged += fee
	if ok {
		b.Ledger.TxSuccess++
		b.Ledger.OpCount += int(t.OpCount)
	} else {
		b.Ledger.TxFailed++
	}

	ops := tx.Envelope.Operations()
	results, _ := tx.Result.OperationResults()
	for i, op := range ops {
		var r *xdr.OperationResult
		if i < len(results) {
			r = &results[i]
		}
		if op.Body.Type == xdr.OperationTypeInvokeHostFunction && op.Body.InvokeHostFunctionOp != nil {
			if ic, ok := op.Body.InvokeHostFunctionOp.HostFunction.GetInvokeContract(); ok {
				t.ContractID, _ = ic.ContractAddress.String()
				t.Function = string(ic.FunctionName)
			}
		}
		opSrc := src
		if op.SourceAccount != nil {
			opSrc = op.SourceAccount.ToAccountId().Address()
		}
		if c.p.opts.Operations && (ok || !c.p.opts.SkipFailed) {
			row := Operation{
				ID:         toid.New(int32(c.seq), int32(tx.Index), int32(i+1)).ToInt64(),
				TxHash:     hash,
				Ledger:     c.seq,
				TxIndex:    tx.Index,
				Index:      uint32(i),
				ClosedAt:   c.closedAt,
				Type:       opTypeName(op.Body.Type),
				Source:     opSrc,
				Successful: ok,
				Details:    c.opDetails(op),
			}
			if r != nil {
				row.ResultCode = opResultCode(*r)
			}
			b.Operations = append(b.Operations, row)
		}
		if ok && r != nil {
			c.classicTrades(hash, tx.Index, uint32(i), opSrc, *r)
		}
	}
	if ok || !c.p.opts.SkipFailed {
		b.Transactions = append(b.Transactions, t)
	}

	transfers := c.transfers(tx, hash)
	c.contractEvents(tx, hash, ok, transfers)
	return nil
}

// classicTrades extracts SDEX and classic AMM fills from offer/path-payment results.
func (c *ledgerCtx) classicTrades(hash string, txIndex, opIndex uint32, taker string, r xdr.OperationResult) {
	if r.Tr == nil {
		return
	}
	var atoms []xdr.ClaimAtom
	switch r.Tr.Type {
	case xdr.OperationTypeManageSellOffer:
		if res, ok := r.Tr.GetManageSellOfferResult(); ok && res.Success != nil {
			atoms = res.Success.OffersClaimed
		}
	case xdr.OperationTypeCreatePassiveSellOffer:
		if res, ok := r.Tr.GetCreatePassiveSellOfferResult(); ok && res.Success != nil {
			atoms = res.Success.OffersClaimed
		}
	case xdr.OperationTypeManageBuyOffer:
		if res, ok := r.Tr.GetManageBuyOfferResult(); ok && res.Success != nil {
			atoms = res.Success.OffersClaimed
		}
	case xdr.OperationTypePathPaymentStrictReceive:
		if res, ok := r.Tr.GetPathPaymentStrictReceiveResult(); ok && res.Success != nil {
			atoms = res.Success.Offers
		}
	case xdr.OperationTypePathPaymentStrictSend:
		if res, ok := r.Tr.GetPathPaymentStrictSendResult(); ok && res.Success != nil {
			atoms = res.Success.Offers
		}
	}
	for i, a := range atoms {
		if a.AmountSold() == 0 || a.AmountBought() == 0 {
			continue
		}
		t := Trade{
			Ledger:       c.seq,
			TxHash:       hash,
			TxIndex:      txIndex,
			OpIndex:      opIndex,
			Index:        uint32(i),
			ClosedAt:     c.closedAt,
			Taker:        taker,
			SoldAsset:    c.asset(a.AssetBought()),
			SoldAmount:   fmt.Sprint(int64(a.AmountBought())),
			BoughtAsset:  c.asset(a.AssetSold()),
			BoughtAmount: fmt.Sprint(int64(a.AmountSold())),
		}
		if a.Type == xdr.ClaimAtomTypeClaimAtomTypeLiquidityPool {
			t.Venue = "amm"
			t.Pool = hex.EncodeToString(a.LiquidityPool.LiquidityPoolId[:])
			t.Maker = t.Pool
		} else {
			t.Venue = "sdex"
			t.Pool = fmt.Sprint(int64(a.OfferId()))
			t.Maker = a.SellerId().Address()
		}
		c.b.Trades = append(c.b.Trades, t)
	}
}

// transfers runs the SDK token-transfer processor (CAP-67 semantics) and
// returns the operation-level transfers keyed by 0-based op index.
func (c *ledgerCtx) transfers(tx ingest.LedgerTransaction, hash string) map[uint32][]Transfer {
	evs, err := c.p.ttp.EventsFromTransaction(tx)
	if err != nil {
		c.warn("token transfers for %s: %v", hash, err)
		return nil
	}
	byOp := map[uint32][]Transfer{}
	idx := uint32(0)
	add := func(e *token_transfer.TokenTransferEvent, keep bool) {
		t := Transfer{Ledger: c.seq, TxHash: hash, TxIndex: tx.Index, ClosedAt: c.closedAt}
		if e.Meta != nil {
			t.OpIndex = e.Meta.OperationIndex
			t.ContractID = e.Meta.ContractAddress
		}
		switch ev := e.Event.(type) {
		case *token_transfer.TokenTransferEvent_Transfer:
			t.Type, t.From, t.To, t.Amount = "transfer", ev.Transfer.From, ev.Transfer.To, ev.Transfer.Amount
		case *token_transfer.TokenTransferEvent_Mint:
			t.Type, t.To, t.Amount = "mint", ev.Mint.To, ev.Mint.Amount
		case *token_transfer.TokenTransferEvent_Burn:
			t.Type, t.From, t.Amount = "burn", ev.Burn.From, ev.Burn.Amount
		case *token_transfer.TokenTransferEvent_Clawback:
			t.Type, t.From, t.Amount = "clawback", ev.Clawback.From, ev.Clawback.Amount
		case *token_transfer.TokenTransferEvent_Fee:
			t.Type, t.From, t.Amount = "fee", ev.Fee.From, ev.Fee.Amount
		default:
			return
		}
		if pa := e.GetAsset(); pa != nil {
			t.Asset = c.asset(pa.ToXdrAsset())
		} else if t.ContractID != "" {
			t.Asset = c.token(t.ContractID)
		}
		t.Index = idx
		idx++
		if keep {
			c.b.Transfers = append(c.b.Transfers, t)
		}
		if t.OpIndex != nil && *t.OpIndex > 0 {
			op := *t.OpIndex - 1
			byOp[op] = append(byOp[op], t)
		}
	}
	for _, e := range evs.FeeEvents {
		add(e, c.p.opts.FeeEvents)
	}
	for _, e := range evs.OperationEvents {
		add(e, true)
	}
	return byOp
}

func (c *ledgerCtx) contractEvents(tx ingest.LedgerTransaction, hash string, ok bool, transfers map[uint32][]Transfer) {
	evs, err := tx.GetTransactionEvents()
	if err != nil {
		c.warn("events for %s: %v", hash, err)
		return
	}
	for opIdx, list := range evs.OperationEvents {
		var swaps []swapCandidate
		for i, ev := range list {
			if ev.Type != xdr.ContractEventTypeContract || ev.Body.V0 == nil || ev.ContractId == nil {
				continue
			}
			contract := contractStrkey(*ev.ContractId)
			topics := ev.Body.V0.Topics
			data := ev.Body.V0.Data
			topic0 := ""
			if len(topics) > 0 {
				topic0, _ = ScValSymbol(topics[0])
			}
			d := c.p.protocols.decode(contract, topics, data)
			std := isTokenEvent(topic0, topics)
			if !std || c.p.opts.TokenEvents {
				jt := make([]any, len(topics))
				for k, tv := range topics {
					jt[k] = ScValToJSON(tv)
				}
				c.b.ContractEvents = append(c.b.ContractEvents, ContractEvent{
					Ledger: c.seq, TxHash: hash, TxIndex: tx.Index, OpIndex: uint32(opIdx), Index: uint32(i),
					ClosedAt: c.closedAt, ContractID: contract, Type: "contract", Topic0: topic0,
					Topics: jt, Data: ScValToJSON(data), Protocol: d.protocol, Successful: ok,
				})
			}
			if d.event != nil {
				pe := *d.event
				pe.Ledger, pe.TxHash, pe.TxIndex, pe.OpIndex, pe.Index, pe.ClosedAt = c.seq, hash, tx.Index, uint32(opIdx), uint32(i), c.closedAt
				pe.Protocol, pe.ContractID = d.protocol, contract
				if pe.Asset != "" && len(pe.Asset) == 56 && pe.Asset[0] == 'C' {
					pe.Asset = c.token(pe.Asset)
				}
				c.b.ProtocolEvents = append(c.b.ProtocolEvents, pe)
			}
			if d.swap != nil {
				s := *d.swap
				s.pool, s.venue, s.eventIndex = contract, d.protocol, uint32(i)
				swaps = append(swaps, s)
			}
		}
		if len(swaps) > 0 && ok {
			c.sorobanTrades(hash, tx.Index, uint32(opIdx), swaps, transfers[uint32(opIdx)])
		}
	}
}

// sorobanTrades resolves swap events into trades. Tokens not named by the
// event itself are inferred from token movements to/from the pool contract
// in the same operation.
func (c *ledgerCtx) sorobanTrades(hash string, txIndex, opIndex uint32, swaps []swapCandidate, moves []Transfer) {
	used := make([]bool, len(moves))
	find := func(pool string, incoming bool, amount string) string {
		match := func(exact bool) int {
			for k, m := range moves {
				if used[k] || m.Type != "transfer" {
					continue
				}
				if (incoming && m.To == pool) || (!incoming && m.From == pool) {
					if !exact || m.Amount == amount {
						return k
					}
				}
			}
			return -1
		}
		k := match(true)
		if k < 0 {
			k = match(false)
		}
		if k < 0 {
			return ""
		}
		used[k] = true
		return moves[k].Asset
	}
	for _, s := range swaps {
		if s.amountIn == nil || s.amountOut == nil || s.amountIn.Sign() <= 0 || s.amountOut.Sign() <= 0 {
			continue
		}
		in, out := s.amountIn.String(), s.amountOut.String()
		tokenIn, tokenOut := "", ""
		if s.tokenIn != "" {
			tokenIn = c.token(s.tokenIn)
		} else {
			tokenIn = find(s.pool, true, in)
		}
		if s.tokenOut != "" {
			tokenOut = c.token(s.tokenOut)
		} else {
			tokenOut = find(s.pool, false, out)
		}
		if tokenIn == "" || tokenOut == "" || tokenIn == tokenOut {
			c.warn("unresolved %s swap in %s op %d (pool %s)", s.venue, hash, opIndex, s.pool)
			continue
		}
		c.b.Trades = append(c.b.Trades, Trade{
			Ledger: c.seq, TxHash: hash, TxIndex: txIndex, OpIndex: opIndex, Index: 1000 + s.eventIndex,
			ClosedAt: c.closedAt, Venue: s.venue, Pool: s.pool, Maker: s.pool, Taker: s.user,
			SoldAsset: tokenIn, SoldAmount: in, BoughtAsset: tokenOut, BoughtAmount: out,
		})
	}
}

func isTokenEvent(topic0 string, topics []xdr.ScVal) bool {
	switch topic0 {
	case "transfer", "mint", "burn", "clawback", "fee", "approve", "set_authorized", "set_admin":
		return len(topics) >= 2
	}
	return false
}
