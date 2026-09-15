package earnwallet

import (
	"encoding/json"
	"io"
	"net/http"
)

// maxCallbackBody bounds what a handler will read. Callbacks are small; an
// unbounded read is a way to exhaust memory on an endpoint that is, by design,
// reachable from outside.
const maxCallbackBody = 1 << 20

// DepositHandler serves the deposit callback endpoint.
//
// It reads the raw body, verifies the signature against those exact bytes, and
// only then decodes. Doing it in that order is the whole point: decoding first
// and re-encoding to verify changes the bytes and the signature stops matching.
//
// Delivery is at-least-once. The same event will arrive again after a network
// timeout even though it was processed, so fn must be idempotent - key it on
// DepositEvent.TxID.
//
// Any 2xx ends delivery. Anything else is retried with backoff for about a day,
// after which the event is parked for an operator and can be replayed by hand.
// So return non-2xx only when a retry could genuinely succeed: answering 500 to
// an event you have permanently rejected buys a day of pointless retries and a
// dead letter someone has to look at.
func DepositHandler(v Verifier, fn func(*http.Request, DepositEvent) error) http.Handler {
	return callbackHandler(v, func(r *http.Request, body []byte) error {
		var event DepositEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return err
		}
		return fn(r, event)
	})
}

// WithdrawalHandler serves the payout callback endpoint, which is a different
// URL from the deposit one. The same ordering and idempotency rules apply; key
// on WithdrawalEvent.ThirdPartyID.
func WithdrawalHandler(v Verifier, fn func(*http.Request, WithdrawalEvent) error) http.Handler {
	return callbackHandler(v, func(r *http.Request, body []byte) error {
		var event WithdrawalEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return err
		}
		return fn(r, event)
	})
}

func callbackHandler(v Verifier, handle func(*http.Request, []byte) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxCallbackBody))
		if err != nil {
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}

		// 400, not 401: a bad signature is never worth retrying, and a 5xx
		// would buy a day of redelivery for a request that cannot become valid.
		if err := v.Verify(r.Header.Get(SignatureHeader), body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := handle(r, body); err != nil {
			// The sender retries anything outside 2xx. This is the branch that
			// asks for that, so return an error from fn only when trying again
			// could work.
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
