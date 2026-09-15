package earnwallet

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// One endpoint answers three questions. These name them, so a call site says
// which one it is asking and cannot ask two at once.
func TestTheThreeWalletModes(t *testing.T) {
	cases := map[string]struct {
		call func(*Client) error
		want GetWalletReq
	}{
		"take a new one": {
			call: func(c *Client) error {
				_, err := c.TakeWallet(context.Background(), "bsc")
				return err
			},
			want: GetWalletReq{Chain: "bsc"},
		},
		"by seed": {
			call: func(c *Client) error {
				_, err := c.WalletBySeed(context.Background(), "bsc", 1001)
				return err
			},
			want: GetWalletReq{Chain: "bsc", Seed: ptr(int64(1001))},
		},
		"by address": {
			call: func(c *Client) error {
				_, err := c.WalletByAddress(context.Background(), "bsc", "0xabc")
				return err
			},
			want: GetWalletReq{Chain: "bsc", Address: ptr("0xabc")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got GetWalletReq
			server := stub(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				writeEnvelope(w, 0, "", GetWalletRes{Seed: 1001, Chain: "bsc", Address: "0xabc"})
			})

			if err := tc.call(dial(t, server.URL)); err != nil {
				t.Fatal(err)
			}
			assertWalletReq(t, got, tc.want)
		})
	}
}

// The seed space is the contract's: seedWallets is keyed by uint32 and the
// address is derived from those four bytes. Taking a uint32 here is what stops
// a negative or oversized value from being sent at all.
func TestTheSeedCeilingSurvives(t *testing.T) {
	var got GetWalletReq
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeEnvelope(w, 0, "", GetWalletRes{})
	})

	const highest uint32 = 4294967295
	if _, err := dial(t, server.URL).WalletBySeed(context.Background(), "bsc", highest); err != nil {
		t.Fatal(err)
	}
	if got.Seed == nil || *got.Seed != int64(highest) {
		t.Errorf("seed = %v, want %d exactly", got.Seed, highest)
	}
}

func assertWalletReq(t *testing.T, got, want GetWalletReq) {
	t.Helper()
	if got.Chain != want.Chain {
		t.Errorf("chain = %q, want %q", got.Chain, want.Chain)
	}
	if (got.Seed == nil) != (want.Seed == nil) || (got.Seed != nil && *got.Seed != *want.Seed) {
		t.Errorf("seed = %v, want %v", got.Seed, want.Seed)
	}
	if (got.Address == nil) != (want.Address == nil) || (got.Address != nil && *got.Address != *want.Address) {
		t.Errorf("address = %v, want %v", got.Address, want.Address)
	}
}

func ptr[T any](v T) *T { return &v }
