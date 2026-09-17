package earnwallet

import (
	"encoding/json"
	"fmt"
	"net/http"
)

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

// Handler serves every callback on one URL, choosing the handler by the event
// field rather than by the path it arrived at.
//
// Use this when the two endpoints would be the same URL. DepositHandler and
// WithdrawalHandler stay the right choice when they are different URLs, which
// is what a receiver whose routing already separates them will want.
//
// Pointing both at one URL without this is the trap it exists to close:
// DepositHandler decodes a payout body into a DepositEvent without complaint,
// because JSON leaves absent fields at their zero value. What reaches the
// deposit path is an event with no address and no amount, and nothing reports
// that anything went wrong.
//
// An event no handler claims is answered with an error rather than a 200. A
// 200 would end delivery, and the event would exist only on the sending side;
// an error parks it as a dead letter an operator can see and replay. So a
// receiver that has not been taught a newly added event type will accumulate
// dead letters - which is the intended outcome, because the alternative on a
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
