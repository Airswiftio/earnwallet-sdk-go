package earnwallet

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type vector struct {
	Name      string `json:"name"`
	Secret    string `json:"secret"`
	Timestamp int64  `json:"timestamp"`
	Body      string `json:"body"`
	Signature string `json:"signature"`
	Header    string `json:"header"`
}

type vectorFile struct {
	HeaderName    string   `json:"header_name"`
	RotatedSecret string   `json:"rotated_secret"`
	Cases         []vector `json:"cases"`
}

func vectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/signature_vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("the vector file is empty; conformance is not being checked")
	}
	return file
}

func caseNamed(t *testing.T, name string) vector {
	t.Helper()
	for _, c := range vectors(t).Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("the vector file has no case named %q", name)
	return vector{}
}

// at returns a Verifier whose clock sits on the vector's own timestamp, so the
// vectors never expire and the suite does not start failing on its own.
func at(ts int64, keys ...Key) Verifier {
	return Verifier{Keys: keys, now: func() time.Time { return time.Unix(ts, 0) }}
}

// The contract both sides implement. A change to the signed material breaks
// this before it breaks an integrator.
func TestVectorsVerify(t *testing.T) {
	for _, c := range vectors(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := Sign(c.Secret, c.Timestamp, []byte(c.Body)); got != c.Signature {
				t.Fatalf("Sign = %s, want %s", got, c.Signature)
			}
			if err := at(c.Timestamp, Key{ID: "k", Secret: c.Secret}).Verify(c.Header, []byte(c.Body)); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

func TestHeaderNameMatchesTheContract(t *testing.T) {
	if name := vectors(t).HeaderName; name != SignatureHeader {
		t.Errorf("SignatureHeader = %q, the contract says %q", SignatureHeader, name)
	}
}

// Rotation is the receiver holding two secrets at once. Without it, rolling a
// secret needs both sides to change in the same instant.
func TestEitherKeyVerifiesDuringRotation(t *testing.T) {
	file := vectors(t)
	rotated := caseNamed(t, "signed_with_rotated_secret")

	v := at(rotated.Timestamp,
		Key{ID: "retiring", Secret: caseNamed(t, "deposit").Secret},
		Key{ID: "incoming", Secret: file.RotatedSecret},
	)
	if err := v.Verify(rotated.Header, []byte(rotated.Body)); err != nil {
		t.Fatalf("a body signed with either held key must verify: %v", err)
	}
}

// A receiver with no secret must reject everything. Waving callbacks through
// until the secret is configured is an endpoint that credits whoever finds it.
func TestNoKeysFailsClosed(t *testing.T) {
	for _, keys := range [][]Key{nil, {{ID: "unset", Secret: ""}}} {
		if err := (Verifier{Keys: keys}).Verify("t=1,v1=ff", []byte("{}")); !errors.Is(err, ErrNoKeys) {
			t.Errorf("Verify with %v = %v, want ErrNoKeys", keys, err)
		}
	}
}

func TestRejections(t *testing.T) {
	c := caseNamed(t, "deposit")
	key := Key{ID: "k", Secret: c.Secret}
	window := int64(DefaultTolerance / time.Second)

	cases := []struct {
		name   string
		header string
		body   string
		clock  int64
		want   error
	}{
		{"a changed body", c.Header, c.Body + " ", c.Timestamp, ErrNoKeyMatched},
		{"a foreign signature", strings.Replace(c.Header, c.Signature, strings.Repeat("a", 64), 1), c.Body, c.Timestamp, ErrNoKeyMatched},
		{"a timestamp older than the window", c.Header, c.Body, c.Timestamp + window + 1, ErrStale},
		{"a timestamp from the future", c.Header, c.Body, c.Timestamp - window - 1, ErrStale},
		{"no header at all", "", c.Body, c.Timestamp, ErrMalformed},
		{"a header with no v1", "t=1786440166", c.Body, c.Timestamp, ErrMissingV1},
		{"an empty v1", "t=1786440166,v1=", c.Body, c.Timestamp, ErrMalformed},
		{"a non-numeric timestamp", "t=yesterday,v1=ff", c.Body, c.Timestamp, ErrMalformed},
		{"a signature that is not hex", "t=1786440166,v1=zz", c.Body, c.Timestamp, ErrMalformed},
		{"a bare token", "nonsense", c.Body, c.Timestamp, ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := at(tc.clock, key).Verify(tc.header, []byte(tc.body)); !errors.Is(err, tc.want) {
				t.Errorf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

// Moving the timestamp forward to defeat the replay window must invalidate the
// signature, because the timestamp is part of the signed material.
func TestReplayCannotBeRefreshedByEditingTheTimestamp(t *testing.T) {
	c := caseNamed(t, "deposit")
	later := c.Timestamp + 600
	forged := strings.Replace(c.Header,
		"t="+strconv.FormatInt(c.Timestamp, 10),
		"t="+strconv.FormatInt(later, 10), 1)

	if err := at(later, Key{ID: "k", Secret: c.Secret}).Verify(forged, []byte(c.Body)); !errors.Is(err, ErrNoKeyMatched) {
		t.Errorf("Verify = %v, want the signature to stop matching", err)
	}
}

func TestDepositHandlerVerifiesBeforeDecoding(t *testing.T) {
	c := caseNamed(t, "deposit")
	called := false
	handler := DepositHandler(at(c.Timestamp, Key{ID: "k", Secret: c.Secret}),
		func(_ *http.Request, event DepositEvent) error {
			called = true
			if event.TxID != "0xabc#3" {
				t.Errorf("TxID = %q", event.TxID)
			}
			if event.Amount != "12.5" || event.RawAmount != "12500000000000000000" {
				t.Errorf("amounts arrived as %q / %q", event.Amount, event.RawAmount)
			}
			return nil
		})

	rec := post(handler, c.Header, c.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if !called {
		t.Error("the callback was never delivered to the handler")
	}
}

// A bad signature can never become valid, so it must not ask for a day of
// retries.
func TestABadSignatureIsNotRetryable(t *testing.T) {
	c := caseNamed(t, "deposit")
	handler := DepositHandler(at(c.Timestamp, Key{ID: "k", Secret: "wrong"}),
		func(*http.Request, DepositEvent) error {
			t.Error("a callback that failed verification reached the handler")
			return nil
		})

	if rec := post(handler, c.Header, c.Body); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 so the sender stops", rec.Code)
	}
}

// The sender retries anything outside 2xx; this is how a receiver asks for one.
func TestAHandlerErrorAsksForRedelivery(t *testing.T) {
	c := caseNamed(t, "deposit")
	handler := DepositHandler(at(c.Timestamp, Key{ID: "k", Secret: c.Secret}),
		func(*http.Request, DepositEvent) error { return errors.New("database is unavailable") })

	if rec := post(handler, c.Header, c.Body); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestWithdrawalHandlerReadsBothOutcomes(t *testing.T) {
	for _, name := range []string{"withdrawal_success", "withdrawal_failed"} {
		t.Run(name, func(t *testing.T) {
			c := caseNamed(t, name)
			var got WithdrawalEvent
			handler := WithdrawalHandler(at(c.Timestamp, Key{ID: "k", Secret: c.Secret}),
				func(_ *http.Request, event WithdrawalEvent) error {
					got = event
					return nil
				})
			if rec := post(handler, c.Header, c.Body); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}
			if got.ExternalID == "" {
				t.Error("ExternalID is what the order is looked up by and must survive decoding")
			}
			if name == "withdrawal_failed" {
				if got.Succeeded() {
					t.Error("a failed payout reported success")
				}
				if got.FailedReason == "" {
					t.Error("a failed payout with no reason is unsupportable")
				}
			} else if !got.Succeeded() {
				t.Error("a successful payout reported failure")
			}
		})
	}
}

// A body larger than the cap must not be silently truncated into something that
// still verifies; truncation changes the bytes, so the signature stops matching.
func TestAnOversizedBodyIsRejected(t *testing.T) {
	secret := "whsec_test"
	body := strings.Repeat("x", maxCallbackBody+1)
	ts := int64(1786440166)
	header := "t=" + strconv.FormatInt(ts, 10) + ",v1=" + Sign(secret, ts, []byte(body))

	handler := DepositHandler(at(ts, Key{ID: "k", Secret: secret}),
		func(*http.Request, DepositEvent) error {
			t.Error("a truncated body reached the handler")
			return nil
		})

	if rec := post(handler, header, body); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestOnlyPostIsAccepted(t *testing.T) {
	handler := DepositHandler(Verifier{Keys: []Key{{ID: "k", Secret: "s"}}},
		func(*http.Request, DepositEvent) error { return nil })

	req := httptest.NewRequest(http.MethodGet, "/callbacks/earnwallet", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func post(handler http.Handler, header, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/callbacks/earnwallet", strings.NewReader(body))
	req.Header.Set(SignatureHeader, header)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// A receiver dispatches on Event rather than on which endpoint the request
// arrived at, and dedupes on EventID whatever the type. Both must survive
// decoding or the SDK has published fields that do not arrive.
func TestEventFieldsSurviveDecoding(t *testing.T) {
	c := caseNamed(t, "deposit")
	var got DepositEvent
	handler := DepositHandler(at(c.Timestamp, Key{ID: "k", Secret: c.Secret}),
		func(_ *http.Request, event DepositEvent) error {
			got = event
			return nil
		})
	if rec := post(handler, c.Header, c.Body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got.Event != EventDepositConfirmed {
		t.Errorf("Event = %q, want %q", got.Event, EventDepositConfirmed)
	}
	if got.EventID == "" {
		t.Error("EventID is the dedupe key and must arrive")
	}
}

func TestWithdrawalEventsCarryTheirOutcomeAsAType(t *testing.T) {
	cases := map[string]string{
		"withdrawal_success": EventWithdrawalSucceeded,
		"withdrawal_failed":  EventWithdrawalFailed,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			c := caseNamed(t, name)
			var got WithdrawalEvent
			handler := WithdrawalHandler(at(c.Timestamp, Key{ID: "k", Secret: c.Secret}),
				func(_ *http.Request, event WithdrawalEvent) error {
					got = event
					return nil
				})
			if rec := post(handler, c.Header, c.Body); rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got.Event != want {
				t.Errorf("Event = %q, want %q", got.Event, want)
			}
			// Event and Status must agree; a receiver switching on one and
			// crediting on the other would otherwise diverge.
			if got.Succeeded() != (want == EventWithdrawalSucceeded) {
				t.Errorf("Event %q disagrees with status %d", got.Event, got.Status)
			}
		})
	}
}
