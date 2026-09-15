package earnwallet

import "context"

// One endpoint answers three questions, and these name them.
//
// GetWallet takes a request struct where seed and address are both optional,
// which leaves two states a caller should never be in: both set, which the
// service rejects, and neither set when a lookup was meant, which quietly
// takes a seed instead of failing. Naming the three modes makes the first
// impossible to express and the second impossible to write by accident.
//
// GetWallet stays available for a caller building the request from data.

// TakeWallet returns a deposit wallet nobody has used, on one chain.
//
// It is not a creation: the address is computed from the factory and the seed,
// and the wallet contract is not deployed until the first collection sweeps
// it. Funds sent before that arrive all the same, and there is no transaction
// to wait for.
//
// There is no idempotency key. A call that times out and is retried takes a
// second seed and abandons the first, which costs nothing because nobody was
// ever told about it - but store the one you got back, not both.
//
// The seed identifies this wallet on every chain, so one more call with
// WalletBySeed gets the address on the next one.
func (c *Client) TakeWallet(ctx context.Context, chain string) (*GetWalletRes, error) {
	return c.GetWallet(ctx, GetWalletReq{Chain: chain})
}

// WalletBySeed returns the wallet a seed owns on one chain. Pure lookup: the
// same seed and chain always give the same address.
//
// The seed is a uint32 because the contract's is - seedWallets is keyed by
// uint32 and the address is derived from those four bytes. The generated
// request carries an int64 because OpenAPI has no unsigned integer, and a
// value that does not fit would be refused on arrival rather than here.
func (c *Client) WalletBySeed(ctx context.Context, chain string, seed uint32) (*GetWalletRes, error) {
	widened := int64(seed)
	return c.GetWallet(ctx, GetWalletReq{Chain: chain, Seed: &widened})
}

// WalletByAddress is the reverse: which seed owns this address. An address
// this service never handed out is not found rather than empty, because the
// two mean different things to whoever is asking.
func (c *Client) WalletByAddress(ctx context.Context, chain, address string) (*GetWalletRes, error) {
	return c.GetWallet(ctx, GetWalletReq{Chain: chain, Address: &address})
}
