package earnwallet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The client is hand-written; the specification is generated. This is what
// stops the two from drifting: an operation added to the service and exported
// into openapi.yaml fails here until a method exists for it.
func TestEveryPublishedOperationHasAMethod(t *testing.T) {
	spec, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}

	matches := regexp.MustCompile(`(?m)^\s*operationId:\s*(\w+)\s*$`).FindAllStringSubmatch(string(spec), -1)
	if len(matches) == 0 {
		// Not "nothing to check": either the export stopped emitting
		// operationId or this pattern went stale, and both would let the whole
		// guard pass silently.
		t.Fatal("no operationId found in openapi.yaml; this test is no longer checking anything")
	}

	client := reflect.TypeOf(&Client{})
	for _, match := range matches {
		operation := match[1]
		t.Run(operation, func(t *testing.T) {
			if _, found := client.MethodByName(operation); !found {
				t.Errorf("the API publishes %s but the client has no such method", operation)
			}
		})
	}
}

func TestNewClientRejectsUnusableBaseURLs(t *testing.T) {
	for _, base := range []string{"", "   ", "earnwallet.internal", "/v1", "://nope"} {
		if _, err := NewClient(base); err == nil {
			t.Errorf("NewClient(%q) was accepted; every request would then fail one at a time", base)
		}
	}
}

func TestTrailingSlashDoesNotDoubleUp(t *testing.T) {
	var path string
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeEnvelope(w, 0, "", GetAddressRes{Address: "0x1"})
	})
	client := dial(t, server.URL+"/")

	if _, err := client.GetAddress(context.Background(), GetAddressParams{Chain: "bsc", Seed: 1}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/addresses" {
		t.Errorf("path = %q, want /v1/addresses", path)
	}
}

func TestGetAddressSendsItsParameters(t *testing.T) {
	var query url.Values
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		writeEnvelope(w, 0, "", GetAddressRes{Address: "0xabc", Chain: "bsc", Seed: 1001})
	})
	client := dial(t, server.URL)

	res, err := client.GetAddress(context.Background(), GetAddressParams{Chain: "bsc", Seed: 1001})
	if err != nil {
		t.Fatal(err)
	}
	if res.Address != "0xabc" {
		t.Errorf("Address = %q", res.Address)
	}
	if got := query.Get("chain"); got != "bsc" {
		t.Errorf("chain = %q", got)
	}
	if got := query.Get("seed"); got != "1001" {
		t.Errorf("seed = %q", got)
	}
	// An absent factory means "the chain's current one". Sending it empty is a
	// different request, and an invalid one.
	if _, present := query["factory"]; present {
		t.Error("factory was sent although it was not set")
	}
}

func TestGetAddressSendsFactoryWhenPinned(t *testing.T) {
	var query url.Values
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		writeEnvelope(w, 0, "", GetAddressRes{})
	})
	client := dial(t, server.URL)

	factory := "0xd7073a2a1884b66852726fed2b9a8511095e3398"
	if _, err := client.GetAddress(context.Background(),
		GetAddressParams{Chain: "bsc", Seed: 1, Factory: &factory}); err != nil {
		t.Fatal(err)
	}
	if got := query.Get("factory"); got != factory {
		t.Errorf("factory = %q, want %q", got, factory)
	}
}

// A business failure arrives as HTTP 200. Reading the status line would call it
// a success.
func TestABusinessFailureIsAnError(t *testing.T) {
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, 40001, "token is not configured for withdrawal", nil)
	})
	client := dial(t, server.URL)

	_, err := client.CreateWithdrawal(context.Background(), CreateWithdrawalReq{ExternalId: "wd-1"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiErr.Code != 40001 {
		t.Errorf("Code = %d", apiErr.Code)
	}
	if !strings.Contains(apiErr.Error(), "token is not configured") {
		t.Errorf("Error() = %q, the message must survive", apiErr.Error())
	}
}

// Anything that is not the envelope is a proxy or the wrong address. Saying so
// beats a decode error nobody can act on.
func TestANonEnvelopeBodyNamesWhatArrived(t *testing.T) {
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>502 Bad Gateway</html>")
	})
	client := dial(t, server.URL)

	_, err := client.GetWithdrawal(context.Background(), "wd-1")
	if err == nil {
		t.Fatal("a gateway error page was accepted")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want it to quote what arrived", err)
	}
}

// A success envelope under a 5xx is a contradiction; taking it at face value
// would report an outcome the service never gave.
func TestASuccessEnvelopeUnderAnErrorStatusIsRefused(t *testing.T) {
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeEnvelope(w, 0, "", CreateWithdrawalRes{Status: "accepted"})
	})
	client := dial(t, server.URL)

	if _, err := client.CreateWithdrawal(context.Background(), CreateWithdrawalReq{ExternalId: "wd-1"}); err == nil {
		t.Fatal("a payout was reported as accepted on a 500")
	}
}

func TestCreateWithdrawalPostsTheOrder(t *testing.T) {
	var body CreateWithdrawalReq
	var contentType string
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeEnvelope(w, 0, "", CreateWithdrawalRes{ExternalId: body.ExternalId, Status: "accepted"})
	})
	client := dial(t, server.URL)

	in := CreateWithdrawalReq{
		ExternalId: "wd-0001", Chain: "bsc", Amount: "100",
		TokenId: "0x55d3", ToAddress: "0x0001",
	}
	res, err := client.CreateWithdrawal(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if body != in {
		t.Errorf("server saw %+v, want %+v", body, in)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if res.Status != "accepted" {
		t.Errorf("Status = %q", res.Status)
	}
}

// The external id goes in the path, so a value containing slashes must not be
// able to reach into the route. It arrives as one segment or not at all.
func TestExternalIdIsEscapedIntoThePath(t *testing.T) {
	const id = "wd/../../admin"

	var escaped, decoded string
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		escaped, decoded = r.URL.EscapedPath(), r.URL.Path
		writeEnvelope(w, 0, "", GetWithdrawalRes{})
	})
	client := dial(t, server.URL)

	if _, err := client.GetWithdrawal(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if want := "/v1/withdrawals/" + url.PathEscape(id); escaped != want {
		t.Errorf("escaped path = %q, want %q", escaped, want)
	}
	// Decoded, it is still one id under the collection rather than a walk out
	// of it.
	if want := "/v1/withdrawals/" + id; decoded != want {
		t.Errorf("decoded path = %q, want %q", decoded, want)
	}
}

// An empty id would address the collection rather than an order - a different
// request, not a failing one - so it has to be refused before it is sent.
func TestABlankPathParameterIsRefusedBeforeSending(t *testing.T) {
	reached := false
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeEnvelope(w, 0, "", GetWithdrawalRes{})
	})
	client := dial(t, server.URL)

	for _, id := range []string{"", "   ", "	"} {
		_, err := client.GetWithdrawal(context.Background(), id)
		if err == nil {
			t.Errorf("GetWithdrawal(%q) was sent", id)
			continue
		}
		if !strings.Contains(err.Error(), "external_id") {
			t.Errorf("err = %v, want it to name the parameter", err)
		}
	}
	if reached {
		t.Error("a request with no id reached the server")
	}
}

// The seam auth will land on, whichever form it takes.
func TestRequestEditorsRunInOrder(t *testing.T) {
	var seen []string
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Values("X-Step")
		writeEnvelope(w, 0, "", AllocateSeedRes{Seed: 7})
	})
	client := dial(t, server.URL,
		WithRequestEditor(func(r *http.Request) error { r.Header.Add("X-Step", "first"); return nil }),
		WithRequestEditor(func(r *http.Request) error { r.Header.Add("X-Step", "second"); return nil }),
	)

	if _, err := client.AllocateSeed(context.Background(), AllocateSeedReq{ExternalRef: "ref"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "first" || seen[1] != "second" {
		t.Errorf("headers = %v, want first then second", seen)
	}
}

func TestAFailingEditorStopsTheRequest(t *testing.T) {
	sent := false
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		sent = true
		writeEnvelope(w, 0, "", AllocateSeedRes{})
	})
	client := dial(t, server.URL,
		WithRequestEditor(func(*http.Request) error { return errors.New("no credential") }))

	if _, err := client.AllocateSeed(context.Background(), AllocateSeedReq{ExternalRef: "ref"}); err == nil {
		t.Fatal("the request went out although the editor failed")
	}
	if sent {
		t.Error("an unsigned request reached the server")
	}
}

// confirmed_at is the one nullable field. Losing its nullability turns "never
// confirmed" into the zero time, which reads as a date in year 1.
func TestNeverConfirmedStaysNil(t *testing.T) {
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"message":"","data":{"external_id":"wd-1",`+
			`"status":"pending","created_at":"2026-09-15T10:00:00Z","confirmed_at":null}}`)
	})
	client := dial(t, server.URL)

	res, err := client.GetWithdrawal(context.Background(), "wd-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ConfirmedAt != nil {
		t.Errorf("ConfirmedAt = %v, want nil for an unconfirmed order", res.ConfirmedAt)
	}
	if res.CreatedAt.IsZero() {
		t.Error("CreatedAt did not decode")
	}
}

func stub(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func dial(t *testing.T, base string, opts ...Option) *Client {
	t.Helper()
	client, err := NewClient(base, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func writeEnvelope(w http.ResponseWriter, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "data": data})
}

// Templates call formatParam instead of carrying a type switch each. A wrong
// rendering here is wrong on every generated method at once.
func TestFormatParam(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{"bsc", "bsc"},
		{"", ""},
		{int64(1001), "1001"},
		{int64(0), "0"},
		{int64(-1), "-1"},
		{int(7), "7"},
		{uint32(4294967295), "4294967295"},
		{true, "true"},
	}
	for _, tc := range cases {
		if got := formatParam(tc.value); got != tc.want {
			t.Errorf("formatParam(%#v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// A seed is documented as fitting in 2^32-1. Rendering it through a float would
// round the high end, so it must go out exactly.
func TestLargeSeedSurvivesTheQueryString(t *testing.T) {
	var query url.Values
	server := stub(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		writeEnvelope(w, 0, "", GetAddressRes{})
	})
	client := dial(t, server.URL)

	if _, err := client.GetAddress(context.Background(),
		GetAddressParams{Chain: "bsc", Seed: 4294967295}); err != nil {
		t.Fatal(err)
	}
	if got := query.Get("seed"); got != "4294967295" {
		t.Errorf("seed = %q, want it exact", got)
	}
}

func TestCheckPathRejectsIncompleteRoutes(t *testing.T) {
	for _, path := range []string{"/v1/withdrawals/{external_id}", "/v1/withdrawals/", "/v1//x"} {
		if err := checkPath(path); err == nil {
			t.Errorf("checkPath(%q) was accepted", path)
		}
	}
	for _, path := range []string{"/v1/addresses", "/v1/withdrawals/wd-1"} {
		if err := checkPath(path); err != nil {
			t.Errorf("checkPath(%q) = %v", path, err)
		}
	}
}

// The template writes {{.OperationId}}Res as every return type, which holds
// only because the exporter derives operationId from that type. If the two ever
// disagree the generated file stops compiling, so this records why.
func TestOperationIdsMatchTheirResponseTypes(t *testing.T) {
	spec, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(?m)^\s*operationId:\s*(\w+)\s*$`).FindAllStringSubmatch(string(spec), -1)
	if len(ids) == 0 {
		t.Fatal("no operationId found; this test is no longer checking anything")
	}
	for _, match := range ids {
		if !regexp.MustCompile(`(?m)^\s{8}` + match[1] + `Res:\s*$`).MatchString(string(spec)) {
			t.Errorf("operationId %s has no %sRes schema to return", match[1], match[1])
		}
	}
}
