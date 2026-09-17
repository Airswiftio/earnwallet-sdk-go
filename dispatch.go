package earnwallet

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxCallbackBody bounds what a handler will read. Callbacks are small; an
// unbounded read is a way to exhaust memory on an endpoint that is, by design,
// reachable from outside.
const maxCallbackBody = 1 << 20

// HandlerOption registers one event handler on Handler.
type HandlerOption func(*dispatcher)

type dispatcher struct {
	deposit    func(*http.Request, DepositEvent) error
	withdrawal func(*http.Request, WithdrawalEvent) error
}

// OnDeposit handles deposit.confirmed.
func OnDeposit(fn func(*http.Request, DepositEvent) error) HandlerOption {
	return func(d *dispatcher) { d.deposit = fn }
}

// OnWithdrawal handles withdrawal.succeeded and withdrawal.failed. Both arrive
// here; WithdrawalEvent.Succeeded reports which.
func OnWithdrawal(fn func(*http.Request, WithdrawalEvent) error) HandlerOption {
	return func(d *dispatcher) { d.withdrawal = fn }
}

// Handler serves the callback endpoint. A tenant has one address, so which
// kind of event arrived is the event field and never the path.
//
// It reads the raw body, verifies the signature against those exact bytes, and
// only then decodes. Doing it in that order is the whole point: decoding first
// and re-encoding to verify changes the bytes and the signature stops matching.
// Only the event field is read before the type is known, because decoding into
// the wrong type succeeds - JSON leaves absent fields at their zero value, so a
// payout read as a deposit arrives with no address and no amount and nothing
// reports it.
//
// Delivery is at-least-once. The same event arrives again after a network
// timeout even though it was processed, so the functions must be idempotent -
// key them on EventID. Not on TxHash: one transaction can pay several deposit
// addresses, so the hash is not unique.
//
// Any 2xx ends delivery. Anything else is retried with backoff for about a day,
// after which the event is parked for an operator and can be replayed by hand.
// So return an error only when a retry could genuinely succeed: answering 500
// to an event permanently rejected buys a day of pointless retries and a dead
// letter someone has to look at.
//
// An event no handler claims is answered with an error rather than a 2xx, for
// that same reason in reverse: a 2xx ends delivery and the event would then
// exist only on the sending side, while an error parks it where an operator can
// find it. A receiver that has not been taught a newly added event type will
// accumulate dead letters, which is the intended outcome - the alternative on a
// money path is losing them quietly.
func Handler(v Verifier, opts ...HandlerOption) http.Handler {
	d := &dispatcher{}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}

	return callbackHandler(v, func(r *http.Request, body []byte) error {
		// Only the discriminator is read here. Decoding into the full type
		// before knowing which type it is would succeed on the wrong one.
		var envelope struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return err
		}

		switch envelope.Event {
		case EventDepositConfirmed:
			if d.deposit == nil {
				return fmt.Errorf("earnwallet: no handler registered for %s", envelope.Event)
			}
			var event DepositEvent
			if err := json.Unmarshal(body, &event); err != nil {
				return err
			}
			return d.deposit(r, event)

		case EventWithdrawalSucceeded, EventWithdrawalFailed:
			if d.withdrawal == nil {
				return fmt.Errorf("earnwallet: no handler registered for %s", envelope.Event)
			}
			var event WithdrawalEvent
			if err := json.Unmarshal(body, &event); err != nil {
				return err
			}
			return d.withdrawal(r, event)

		case "":
			return fmt.Errorf("earnwallet: callback carries no event field")

		default:
			return fmt.Errorf("earnwallet: unknown event %q", envelope.Event)
		}
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
