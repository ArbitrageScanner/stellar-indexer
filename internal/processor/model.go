package processor

import "time"

// Batch holds every row derived from one ledger. It is written atomically
// together with the cursor, so a ledger is either fully indexed or not at all.
type Batch struct {
	Ledger         Ledger
	Transactions   []Transaction
	Operations     []Operation
	Assets         []Asset
	Trades         []Trade
	Transfers      []Transfer
	ContractEvents []ContractEvent
	ProtocolEvents []ProtocolEvent
	Accounts       []Account
	Trustlines     []Trustline
	Pools          []Pool
	Offers         []Offer
	// Codes are contract executables created or changed in this ledger; the
	// verifier adds RPC-resolved ones before the batch is written.
	Codes []ContractCode
}

// ContractCode is the executable behind a contract instance.
type ContractCode struct {
	ContractID string
	Executable string // wasm | stellar_asset | missing
	WasmHash   string // hex, for wasm
	Ledger     uint32 // ledger of the change; 0 when resolved via RPC
}

type Ledger struct {
	Sequence       uint32
	Hash           string
	PrevHash       string
	ClosedAt       time.Time
	Protocol       uint32
	TxCount        int
	TxSuccess      int
	TxFailed       int
	OpCount        int
	SorobanTxCount int
	BaseFee        uint32
	BaseReserve    uint32
	TotalCoins     int64
	FeePool        int64
	FeesCharged    int64
}

type Transaction struct {
	Hash         string
	Ledger       uint32
	Index        uint32 // 1-based application order
	ID           int64  // SEP-35 TOID
	ClosedAt     time.Time
	Source       string
	FeeAccount   string
	SeqNum       int64
	FeeCharged   int64
	MaxFee       int64
	OpCount      uint32
	Successful   bool
	ResultCode   string
	MemoType     string
	Memo         string
	IsSoroban    bool
	ContractID   string
	Function     string
	ResourceFee  int64
	Instructions uint32
	InnerHash    string
}

type Operation struct {
	ID         int64
	TxHash     string
	Ledger     uint32
	TxIndex    uint32
	Index      uint32 // 0-based within tx
	ClosedAt   time.Time
	Type       string
	Source     string
	Successful bool
	ResultCode string
	Details    map[string]any
}

type Asset struct {
	Key        string // "native", "CODE:ISSUER" or contract strkey for custom tokens
	Type       string // native | credit_alphanum4 | credit_alphanum12 | contract
	Code       string
	Issuer     string
	ContractID string
	FirstSeen  uint32
}

// Trade is one fill, described from the taker's perspective.
type Trade struct {
	Ledger       uint32
	TxHash       string
	TxIndex      uint32
	OpIndex      uint32
	Index        uint32 // order within the operation
	ClosedAt     time.Time
	Venue        string // sdex | amm | soroswap | aquarius | clmm | amm_generic
	Pool         string // offer id, liquidity pool id, or pool contract
	Maker        string
	Taker        string
	SoldAsset    string // what the taker gave
	SoldAmount   string // integer in the token's base units
	BoughtAsset  string
	BoughtAmount string
	// Verified is true for classic venues and for Soroban pools whose code is
	// allowlisted for the venue's protocol; only verified fills feed prices.
	Verified bool
}

type Transfer struct {
	Ledger     uint32
	TxHash     string
	TxIndex    uint32
	OpIndex    *uint32 // 1-based per SEP-35; nil for fee events
	Index      uint32
	ClosedAt   time.Time
	Type       string // transfer | mint | burn | clawback | fee
	From       string
	To         string
	Asset      string
	ContractID string
	Amount     string
}

type ContractEvent struct {
	Ledger     uint32
	TxHash     string
	TxIndex    uint32
	OpIndex    uint32
	Index      uint32
	ClosedAt   time.Time
	ContractID string
	Type       string
	Topic0     string
	Topics     []any
	Data       any
	Protocol   string
	Verified   bool
	Successful bool
}

type ProtocolEvent struct {
	Ledger     uint32
	TxHash     string
	TxIndex    uint32
	OpIndex    uint32
	Index      uint32
	ClosedAt   time.Time
	Protocol   string
	ContractID string
	Action     string
	User       string
	Asset      string
	Amount0    string
	Amount1    string
	Data       any
	Verified   bool
}

type Account struct {
	ID            string
	Balance       int64
	BuyingLiab    int64
	SellingLiab   int64
	SeqNum        int64
	NumSubentries uint32
	NumSponsoring uint32
	NumSponsored  uint32
	Flags         uint32
	HomeDomain    string
	MasterWeight  uint8
	NumSigners    int
	Sponsor       string
	LastModified  uint32
	Deleted       bool
}

type Trustline struct {
	AccountID    string
	Asset        string
	Balance      int64
	Limit        int64
	BuyingLiab   int64
	SellingLiab  int64
	Flags        uint32
	LastModified uint32
	Deleted      bool
}

type Pool struct {
	ID             string
	AssetA         string
	AssetB         string
	ReserveA       int64
	ReserveB       int64
	TotalShares    int64
	TrustlineCount int64
	FeeBP          int32
	LastModified   uint32
	Deleted        bool
}

type Offer struct {
	ID           int64
	Seller       string
	Selling      string
	Buying       string
	Amount       int64
	PriceN       int32
	PriceD       int32
	Passive      bool
	LastModified uint32
	Deleted      bool
}
