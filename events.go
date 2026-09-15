package earnwallet

// NativeTransferIndex marks a deposit that carries no token log - a plain
// value transfer. It keeps (TxHash, TransferIndex) distinct from a token
// transfer at index 0 of the same transaction.
const NativeTransferIndex int64 = -1

// Event names. Dispatch on these rather than on which endpoint the request
// arrived at: a fourth event type is additive, a fourth endpoint is a
// deployment.
const (
	EventDepositConfirmed    = "deposit.confirmed"
	EventWithdrawalSucceeded = "withdrawal.succeeded"
	EventWithdrawalFailed    = "withdrawal.failed"
)

// DepositEvent reports a confirmed incoming transfer.
//
// TxHash is the plain transaction hash and is NOT unique on its own: one
// transaction can pay several deposit addresses. Dedupe on EventID, which
// carries the transfer index as well.
//
// Amount and RawAmount are decimal strings, never numbers: a float64 cannot
// hold an 18-decimal token amount without losing the low digits. Decimals
// travels so the relationship Amount == RawAmount / 10^Decimals can be checked
// rather than trusted.
//
// Chain is the identifier, and there is no numeric one: EIP-155 ids exist on
// EVM only, and the numbers Solana and Tron used to carry here were a vendor's
// invention rather than the chains'. A chain name says which network it is, so
// a testnet deployment reports "bsc-testnet" rather than "bsc".
//
// Address is the deposit address, which is how the paying account is resolved.
// FromAddress is the on-chain sender, which is a different thing and is what
// risk and compliance need.
//
// BlockTime and Timestamp are both unix seconds. Timestamp is written once
// when the deposit is credited and replayed unchanged on every redelivery, so
// it does not drift across retries.
type DepositEvent struct {
	// Event is always EventDepositConfirmed today. EventID is stable across
	// redeliveries and is the one key to dedupe on whatever the type: the same
	// value appears on the reconciliation endpoint, so catching up after an
	// outage is a set difference rather than re-derived keys.
	Event   string `json:"event"`
	EventID string `json:"event_id"`

	TxHash        string `json:"tx_hash"`
	TransferIndex int64  `json:"transfer_index"`
	Address       string `json:"address"`
	FromAddress   string `json:"from_address"`
	Amount        string `json:"amount"`
	RawAmount     string `json:"raw_amount"`
	Chain         string `json:"chain"`
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
// ExternalID echoes back the external_id the order was submitted with,
// unchanged, and is how the order is looked up.
//
// TxHash is empty on failure: a payout that never broadcast has no hash.
// FailedReason then carries why, and is absent on success.
type WithdrawalEvent struct {
	// Event is EventWithdrawalSucceeded or EventWithdrawalFailed. A payout
	// refused by an operator arrives as failed, with the reason.
	Event   string `json:"event"`
	EventID string `json:"event_id"`

	ExternalID   string `json:"external_id"`
	Amount       string `json:"amount"`
	Chain        string `json:"chain"`
	Currency     string `json:"currency"`
	TokenID      string `json:"token_id"`
	BlockHeight  string `json:"block_height"`
	Timestamp    string `json:"timestamp"`
	FailedReason string `json:"failed_reason,omitempty"`
}

// Succeeded reports whether the payout moved funds. Event is the only thing
// that says so; there is no second status field to disagree with it.
func (e WithdrawalEvent) Succeeded() bool { return e.Event == EventWithdrawalSucceeded }
