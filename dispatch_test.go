package earnwallet_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	earnwallet "github.com/Airswiftio/earnwallet-sdk-go"
)

const dispatchSecret = "whsec_test"

func signed(t *testing.T, body string) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/callbacks/earnwallet", strings.NewReader(body))
	req.Header.Set(earnwallet.SignatureHeader,
		"t="+strconv.FormatInt(ts, 10)+",v1="+earnwallet.Sign(dispatchSecret, ts, []byte(body)))
	return req
}

func verifier() earnwallet.Verifier {
	return earnwallet.Verifier{Keys: []earnwallet.Key{{ID: "t", Secret: dispatchSecret}}}
}

const depositBody = `{"event":"deposit.confirmed","event_id":"dep_1","address":"0x28eb","amount":"5","raw_amount":"5000000","decimals":6}`
const withdrawalOKBody = `{"event":"withdrawal.succeeded","event_id":"wd_1","external_id":"wd-0001","amount":"100"}`
const withdrawalFailBody = `{"event":"withdrawal.failed","event_id":"wd_2","external_id":"wd-0002","failed_reason":"insufficient balance"}`

func TestOneEndpointRoutesByEvent(t *testing.T) {
	var gotDeposit earnwallet.DepositEvent
	var gotWithdrawal earnwallet.WithdrawalEvent

	h := earnwallet.Handler(verifier(),
		earnwallet.OnDeposit(func(_ *http.Request, e earnwallet.DepositEvent) error {
			gotDeposit = e
			return nil
		}),
		earnwallet.OnWithdrawal(func(_ *http.Request, e earnwallet.WithdrawalEvent) error {
			gotWithdrawal = e
			return nil
		}),
	)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, signed(t, depositBody))
	if w.Code != http.StatusOK {
		t.Fatalf("deposit: got %d, want 200 (%s)", w.Code, w.Body)
	}
	if gotDeposit.Address != "0x28eb" || gotDeposit.RawAmount != "5000000" {
		t.Fatalf("deposit decoded wrong: %+v", gotDeposit)
	}
	if gotWithdrawal.EventID != "" {
		t.Fatal("a deposit reached the withdrawal handler")
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, signed(t, withdrawalOKBody))
	if w.Code != http.StatusOK {
		t.Fatalf("withdrawal: got %d, want 200 (%s)", w.Code, w.Body)
	}
	if gotWithdrawal.ExternalID != "wd-0001" || !gotWithdrawal.Succeeded() {
		t.Fatalf("withdrawal decoded wrong: %+v", gotWithdrawal)
	}
}

func TestFailedPayoutReachesTheSameHandler(t *testing.T) {
	var got earnwallet.WithdrawalEvent
	h := earnwallet.Handler(verifier(),
		earnwallet.OnWithdrawal(func(_ *http.Request, e earnwallet.WithdrawalEvent) error {
			got = e
			return nil
		}),
	)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signed(t, withdrawalFailBody))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if got.Succeeded() || got.FailedReason == "" {
		t.Fatalf("a failure decoded as %+v", got)
	}
}

// An event nobody claims must not be answered with a 200: that ends delivery
// and the event survives only on the sending side.
func TestUnclaimedEventIsNotAccepted(t *testing.T) {
	h := earnwallet.Handler(verifier(),
		earnwallet.OnDeposit(func(*http.Request, earnwallet.DepositEvent) error { return nil }),
	)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signed(t, withdrawalOKBody))
	if w.Code == http.StatusOK {
		t.Fatal("a payout with no registered handler was accepted")
	}
}

func TestUnknownEventIsNotAccepted(t *testing.T) {
	h := earnwallet.Handler(verifier(),
		earnwallet.OnDeposit(func(*http.Request, earnwallet.DepositEvent) error { return nil }),
	)
	for _, body := range []string{
		`{"event":"deposit.reversed","event_id":"x"}`,
		`{"event_id":"x"}`,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signed(t, body))
		if w.Code == http.StatusOK {
			t.Fatalf("%s was accepted", body)
		}
	}
}

// The signature still gates everything; dispatch happens after it, never before.
func TestBadSignatureNeverReachesDispatch(t *testing.T) {
	reached := false
	h := earnwallet.Handler(verifier(),
		earnwallet.OnDeposit(func(*http.Request, earnwallet.DepositEvent) error {
			reached = true
			return nil
		}),
	)
	req := httptest.NewRequest(http.MethodPost, "/c", strings.NewReader(depositBody))
	req.Header.Set(earnwallet.SignatureHeader, "t=1,v1=deadbeef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if reached {
		t.Fatal("dispatch ran on an unverified body")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
}
