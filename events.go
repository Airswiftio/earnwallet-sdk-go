package earnwallet

// StatusDepositConfirmed is the only status a deposit callback carries. An
// event exists because the deposit reached its chain's confirmation depth and
// survived reorg detection, so there is no pending or failed variant.
const StatusDepositConfirmed int64 = 1

// Terminal payout outcomes. Only these two are delivered: an intermediate
// "broadcast" event would be a delivery that changes nothing.
const (
	StatusWithdrawalSucceeded int64 = 0
	StatusWithdrawalFailed    int64 = 2
)

// NativeTransferIndex marks a deposit that carries no token log - a plain
// value transfer. It keeps (TxHash, TransferIndex) distinct from a token
// transfer at index 0 of the same transaction.
const NativeTransferIndex int64 = -1

// DepositEvent reports a confirmed incoming transfer.
//
// TxID is "<tx hash>#<transfer index>", not a bare hash, and it is the value to
// key idempotency on. One transaction can pay several deposit addresses; a bare
// hash would make those collide and the second be dropped as already seen.
// TxHash carries the plain hash for chain explorer lookups.
//
// Amount and RawAmount are decimal strings, never numbers: a float64 cannot
// hold an 18-decimal token amount without losing the low digits. Decimals
// travels so the relationship Amount == RawAmount / 10^Decimals can be checked
// rather than trusted.
//
// Address is the deposit address, which is how the paying account is resolved.
// FromAddress is the on-chain sender, which is a different thing and is what
// risk and compliance need.
//
// BlockTime is unix seconds; Timestamp is unix milliseconds. Timestamp is
// written once when the deposit is credited and replayed unchanged on every
// redelivery, so it does not drift across retries.
type DepositEvent struct {
	TxID          string `json:"txid"`
	TxHash        string `json:"tx_hash"`
	TransferIndex int64  `json:"transfer_index"`
	Address       string `json:"address"`
	FromAddress   string `json:"from_address"`
	Amount        string `json:"amount"`
	RawAmount     string `json:"raw_amount"`
	Status        int64  `json:"status"`
	Chain         string `json:"chain"`
	ChainID       string `json:"chain_id"`
	Currency      string `json:"currency"`
	TokenID       string `json:"token_id"`
	Decimals      uint8  `json:"decimals"`
	BlockHeight   string `json:"block_height"`
	BlockTime     string `json:"block_time"`
	Confirmations int    `json:"confirmations"`
	Timestamp     string `json:"timestamp"`
}

// IsNative reports whether the deposit was a plain value transfer rather than a
// token transfer.
func (e DepositEvent) IsNative() bool { return e.TransferIndex == NativeTransferIndex }

// WithdrawalEvent reports the terminal outcome of a payout.
//
// ThirdPartyID echoes back the external_id the order was submitted with,
// unchanged, and is how the order is looked up.
//
// TxID is empty on failure: a payout that never broadcast has no hash.
// FailedReason then carries why, and is absent on success.
type WithdrawalEvent struct {
	ThirdPartyID string `json:"third_party_id"`
	Status       int64  `json:"status"`
	Amount       string `json:"amount"`
	TxID         string `json:"txid"`
	Chain        string `json:"chain"`
	ChainID      string `json:"chain_id"`
	Currency     string `json:"currency"`
	TokenID      string `json:"token_id"`
	BlockHeight  string `json:"block_height"`
	Timestamp    string `json:"timestamp"`
	FailedReason string `json:"failed_reason,omitempty"`
}

// Succeeded reports whether the payout moved funds.
func (e WithdrawalEvent) Succeeded() bool { return e.Status == StatusWithdrawalSucceeded }
