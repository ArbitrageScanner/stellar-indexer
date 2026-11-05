package processor

import (
	"encoding/hex"
	"io"

	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// state emits the final per-ledger value of every account, trustline,
// liquidity pool and offer touched in the ledger (compacted changes).
func (c *ledgerCtx) state(lcm xdr.LedgerCloseMeta) error {
	lr, err := ingest.NewLedgerChangeReaderFromLedgerCloseMeta(c.p.passphrase, lcm)
	if err != nil {
		return err
	}
	reader := ingest.NewCompactingChangeReader(lr, ingest.ChangeCompactorConfig{SuppressRemoveAfterRestoreChange: true})
	defer reader.Close()
	for {
		ch, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		entry := ch.Post
		deleted := false
		if entry == nil {
			entry, deleted = ch.Pre, true
		}
		if entry == nil {
			continue
		}
		lm := uint32(entry.LastModifiedLedgerSeq)
		if deleted {
			lm = c.seq
		}
		switch ch.Type {
		case xdr.LedgerEntryTypeAccount:
			a := entry.Data.MustAccount()
			liab := a.Liabilities()
			row := Account{
				ID: a.AccountId.Address(), Balance: int64(a.Balance), SeqNum: int64(a.SeqNum),
				BuyingLiab: int64(liab.Buying), SellingLiab: int64(liab.Selling),
				NumSubentries: uint32(a.NumSubEntries), NumSponsoring: uint32(a.NumSponsoring()), NumSponsored: uint32(a.NumSponsored()),
				Flags: uint32(a.Flags), HomeDomain: string(a.HomeDomain), MasterWeight: a.MasterKeyWeight(),
				NumSigners: len(a.Signers), LastModified: lm, Deleted: deleted,
			}
			if s := entry.SponsoringID(); s != nil {
				row.Sponsor = s.Address()
			}
			c.b.Accounts = append(c.b.Accounts, row)
		case xdr.LedgerEntryTypeTrustline:
			t := entry.Data.MustTrustLine()
			liab := t.Liabilities()
			key := ""
			if t.Asset.Type == xdr.AssetTypeAssetTypePoolShare {
				key = trustLineAssetKey(t.Asset)
			} else {
				key = c.asset(t.Asset.ToAsset())
			}
			c.b.Trustlines = append(c.b.Trustlines, Trustline{
				AccountID: t.AccountId.Address(), Asset: key, Balance: int64(t.Balance), Limit: int64(t.Limit),
				BuyingLiab: int64(liab.Buying), SellingLiab: int64(liab.Selling), Flags: uint32(t.Flags),
				LastModified: lm, Deleted: deleted,
			})
		case xdr.LedgerEntryTypeLiquidityPool:
			lp := entry.Data.MustLiquidityPool()
			cp := lp.Body.ConstantProduct
			if cp == nil {
				continue
			}
			c.b.Pools = append(c.b.Pools, Pool{
				ID: hex.EncodeToString(lp.LiquidityPoolId[:]), AssetA: c.asset(cp.Params.AssetA), AssetB: c.asset(cp.Params.AssetB),
				ReserveA: int64(cp.ReserveA), ReserveB: int64(cp.ReserveB), TotalShares: int64(cp.TotalPoolShares),
				TrustlineCount: int64(cp.PoolSharesTrustLineCount), FeeBP: int32(cp.Params.Fee), LastModified: lm, Deleted: deleted,
			})
		case xdr.LedgerEntryTypeOffer:
			o := entry.Data.MustOffer()
			c.b.Offers = append(c.b.Offers, Offer{
				ID: int64(o.OfferId), Seller: o.SellerId.Address(), Selling: c.asset(o.Selling), Buying: c.asset(o.Buying),
				Amount: int64(o.Amount), PriceN: int32(o.Price.N), PriceD: int32(o.Price.D),
				Passive: o.Flags&xdr.Uint32(xdr.OfferEntryFlagsPassiveFlag) != 0, LastModified: lm, Deleted: deleted,
			})
		}
	}
}
