package processor

import (
	"encoding/hex"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func opTypeName(t xdr.OperationType) string {
	// "OperationTypeManageSellOffer" -> "manage_sell_offer"
	return snake(strings.TrimPrefix(t.String(), "OperationType"))
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func opResultCode(r xdr.OperationResult) string {
	if r.Code != xdr.OperationResultCodeOpInner || r.Tr == nil {
		return r.Code.String()
	}
	code, err := r.Tr.MapOperationResultTr()
	if err != nil {
		return "unknown"
	}
	return code
}

func muxed(m xdr.MuxedAccount) string { return m.ToAccountId().Address() }

func price(p xdr.Price) float64 {
	if p.D == 0 {
		return 0
	}
	return float64(p.N) / float64(p.D)
}

func (c *ledgerCtx) path(path []xdr.Asset) []string {
	out := make([]string, len(path))
	for i, a := range path {
		out[i] = c.asset(a)
	}
	return out
}

// opDetails extracts the analytically useful fields of each operation.
func (c *ledgerCtx) opDetails(op xdr.Operation) map[string]any {
	b := op.Body
	switch b.Type {
	case xdr.OperationTypeCreateAccount:
		o := b.MustCreateAccountOp()
		return map[string]any{"destination": o.Destination.Address(), "starting_balance": int64(o.StartingBalance)}
	case xdr.OperationTypePayment:
		o := b.MustPaymentOp()
		return map[string]any{"to": muxed(o.Destination), "asset": c.asset(o.Asset), "amount": int64(o.Amount)}
	case xdr.OperationTypePathPaymentStrictReceive:
		o := b.MustPathPaymentStrictReceiveOp()
		return map[string]any{"to": muxed(o.Destination), "send_asset": c.asset(o.SendAsset), "send_max": int64(o.SendMax),
			"dest_asset": c.asset(o.DestAsset), "dest_amount": int64(o.DestAmount), "path": c.path(o.Path)}
	case xdr.OperationTypePathPaymentStrictSend:
		o := b.MustPathPaymentStrictSendOp()
		return map[string]any{"to": muxed(o.Destination), "send_asset": c.asset(o.SendAsset), "send_amount": int64(o.SendAmount),
			"dest_asset": c.asset(o.DestAsset), "dest_min": int64(o.DestMin), "path": c.path(o.Path)}
	case xdr.OperationTypeManageSellOffer:
		o := b.MustManageSellOfferOp()
		return map[string]any{"selling": c.asset(o.Selling), "buying": c.asset(o.Buying), "amount": int64(o.Amount),
			"price": price(o.Price), "offer_id": int64(o.OfferId)}
	case xdr.OperationTypeManageBuyOffer:
		o := b.MustManageBuyOfferOp()
		return map[string]any{"selling": c.asset(o.Selling), "buying": c.asset(o.Buying), "buy_amount": int64(o.BuyAmount),
			"price": price(o.Price), "offer_id": int64(o.OfferId)}
	case xdr.OperationTypeCreatePassiveSellOffer:
		o := b.MustCreatePassiveSellOfferOp()
		return map[string]any{"selling": c.asset(o.Selling), "buying": c.asset(o.Buying), "amount": int64(o.Amount), "price": price(o.Price)}
	case xdr.OperationTypeChangeTrust:
		o := b.MustChangeTrustOp()
		d := map[string]any{"limit": int64(o.Limit)}
		if o.Line.Type == xdr.AssetTypeAssetTypePoolShare {
			if id, err := xdr.NewPoolId(o.Line.LiquidityPool.ConstantProduct.AssetA, o.Line.LiquidityPool.ConstantProduct.AssetB, o.Line.LiquidityPool.ConstantProduct.Fee); err == nil {
				d["asset"] = "pool:" + hex.EncodeToString(id[:])
			}
		} else {
			d["asset"] = c.asset(o.Line.ToAsset())
		}
		return d
	case xdr.OperationTypeAccountMerge:
		return map[string]any{"into": muxed(b.MustDestination())}
	case xdr.OperationTypeManageData:
		o := b.MustManageDataOp()
		return map[string]any{"name": string(o.DataName), "deleted": o.DataValue == nil}
	case xdr.OperationTypeBumpSequence:
		return map[string]any{"bump_to": int64(b.MustBumpSequenceOp().BumpTo)}
	case xdr.OperationTypeSetOptions:
		o := b.MustSetOptionsOp()
		d := map[string]any{}
		if o.HomeDomain != nil {
			d["home_domain"] = string(*o.HomeDomain)
		}
		if o.Signer != nil {
			d["signer_weight"] = uint32(o.Signer.Weight)
		}
		if o.MasterWeight != nil {
			d["master_weight"] = uint32(*o.MasterWeight)
		}
		return d
	case xdr.OperationTypeCreateClaimableBalance:
		o := b.MustCreateClaimableBalanceOp()
		return map[string]any{"asset": c.asset(o.Asset), "amount": int64(o.Amount), "claimants": len(o.Claimants)}
	case xdr.OperationTypeClawback:
		o := b.MustClawbackOp()
		return map[string]any{"from": muxed(o.From), "asset": c.asset(o.Asset), "amount": int64(o.Amount)}
	case xdr.OperationTypeSetTrustLineFlags:
		o := b.MustSetTrustLineFlagsOp()
		return map[string]any{"trustor": o.Trustor.Address(), "asset": c.asset(o.Asset), "set": uint32(o.SetFlags), "clear": uint32(o.ClearFlags)}
	case xdr.OperationTypeLiquidityPoolDeposit:
		o := b.MustLiquidityPoolDepositOp()
		return map[string]any{"pool": hex.EncodeToString(o.LiquidityPoolId[:]), "max_a": int64(o.MaxAmountA), "max_b": int64(o.MaxAmountB)}
	case xdr.OperationTypeLiquidityPoolWithdraw:
		o := b.MustLiquidityPoolWithdrawOp()
		return map[string]any{"pool": hex.EncodeToString(o.LiquidityPoolId[:]), "shares": int64(o.Amount)}
	case xdr.OperationTypeInvokeHostFunction:
		o := b.MustInvokeHostFunctionOp()
		d := map[string]any{"host_function": snake(strings.TrimPrefix(o.HostFunction.Type.String(), "HostFunctionTypeHostFunctionType")), "auth_entries": len(o.Auth)}
		if ic, ok := o.HostFunction.GetInvokeContract(); ok {
			d["contract"], _ = ic.ContractAddress.String()
			d["function"] = string(ic.FunctionName)
			args := make([]any, len(ic.Args))
			for i, a := range ic.Args {
				args[i] = ScValToJSON(a)
			}
			d["args"] = args
		}
		return d
	case xdr.OperationTypeExtendFootprintTtl:
		return map[string]any{"extend_to": uint32(b.MustExtendFootprintTtlOp().ExtendTo)}
	}
	return map[string]any{}
}
