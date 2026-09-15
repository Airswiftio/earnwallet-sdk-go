package earnwallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client calls the EarnWallet API.
//
// It is written rather than generated so the SDK carries no third-party
// dependencies: generating it pulls in a runtime package for query-string
// encoding, and with it two libraries this API has no use for. The request and
// response types in models.gen.go are still generated from the service's own
// specification, which is where the churn actually is - a field changes far
// more often than a route appears.
//
// TestEveryPublishedOperationHasAMethod keeps the two in step.
type Client struct {
	baseURL string
	http    *http.Client
	editors []RequestEditor
}

// RequestEditor runs on every outgoing request before it is sent. It is the
// seam for whatever the deployment needs on the wire - a credential, a trace
// header, a tenant marker - so adding one later is not a breaking change here.
type RequestEditor func(*http.Request) error

type Option func(*Client)

// WithHTTPClient replaces the transport. Use it to set timeouts, proxies or a
// connection pool; the default client has no timeout, which is rarely what a
// service wants.
func WithHTTPClient(doer *http.Client) Option {
	return func(c *Client) {
		if doer != nil {
			c.http = doer
		}
	}
}

// WithRequestEditor appends an editor. Editors run in the order they were added.
func WithRequestEditor(edit RequestEditor) Option {
	return func(c *Client) {
		if edit != nil {
			c.editors = append(c.editors, edit)
		}
	}
}

// NewClient returns a client for the service at baseURL.
func NewClient(baseURL string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("earnwallet: a base url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("earnwallet: base url %q: %w", baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("earnwallet: base url %q needs a scheme and a host", baseURL)
	}

	client := &Client{baseURL: trimmed, http: &http.Client{}}
	for _, opt := range opts {
		opt(client)
	}
	return client, nil
}

// APIError is a business failure. It arrives as HTTP 200 with a non-zero code,
// which is why it cannot be read off the status line.
type APIError struct {
	Code       int
	Message    string
	StatusCode int
}

func (e *APIError) Error() string {
	return "earnwallet: " + strconv.Itoa(e.Code) + " " + e.Message
}

// GetAddress derives the deposit address for a seed on a chain. Derivation is
// arithmetic, so the address is returned whether or not anything has been sent
// to it.
func (c *Client) GetAddress(ctx context.Context, in GetAddressReq) (*GetAddressRes, error) {
	query := url.Values{}
	query.Set("chain", in.Chain)
	query.Set("seed", strconv.FormatInt(in.Seed, 10))
	if in.Factory != nil {
		// Omitted rather than sent empty: the service reads an absent factory
		// as "the chain's current one", and an empty string as a bad address.
		query.Set("factory", *in.Factory)
	}
	return send[GetAddressRes](ctx, c, http.MethodGet, "/v1/addresses", query, nil)
}

// ResolveAddress is the reverse lookup: which seed owns this address.
func (c *Client) ResolveAddress(ctx context.Context, in ResolveAddressReq) (*ResolveAddressRes, error) {
	query := url.Values{}
	query.Set("chain", in.Chain)
	query.Set("address", in.Address)
	return send[ResolveAddressRes](ctx, c, http.MethodGet, "/v1/addresses/resolve", query, nil)
}

// AllocateSeed binds a seed to the caller's own reference. ExternalRef is the
// idempotency key: allocating twice with the same one returns the same seed
// rather than burning a second.
func (c *Client) AllocateSeed(ctx context.Context, in AllocateSeedReq) (*AllocateSeedRes, error) {
	return send[AllocateSeedRes](ctx, c, http.MethodPost, "/v1/seeds", nil, in)
}

// CreateWithdrawal submits a payout order.
//
// ExternalId is the idempotency key and it matters more here than anywhere
// else: a timed-out submission may already have created the order, and
// resubmitting under a new id pays twice. Retry with the same one.
func (c *Client) CreateWithdrawal(ctx context.Context, in CreateWithdrawalReq) (*CreateWithdrawalRes, error) {
	return send[CreateWithdrawalRes](ctx, c, http.MethodPost, "/v1/withdrawals", nil, in)
}

// GetWithdrawal reads one order. It is for looking into a specific order, not
// for reconciling: the terminal state arrives by callback, which has an outbox
// behind it, and polling has nothing.
func (c *Client) GetWithdrawal(ctx context.Context, externalID string) (*GetWithdrawalRes, error) {
	if strings.TrimSpace(externalID) == "" {
		return nil, fmt.Errorf("earnwallet: an external id is required")
	}
	return send[GetWithdrawalRes](ctx, c, http.MethodGet,
		"/v1/withdrawals/"+url.PathEscape(externalID), nil, nil)
}

// envelope is what every endpoint answers with. Decoding it in one place is
// half the reason this file is written by hand: a generated client hands the
// envelope back and every call site has to unwrap it the same way.
type envelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func send[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any) (*T, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("earnwallet: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, edit := range c.editors {
		if err := edit(req); err != nil {
			return nil, fmt.Errorf("earnwallet: edit request: %w", err)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// The request may still have been served. For CreateWithdrawal that
		// means the order can exist; retry with the same ExternalId.
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("earnwallet: read response: %w", err)
	}

	var decoded envelope[T]
	if err := json.Unmarshal(payload, &decoded); err != nil {
		// A body that is not the envelope is a proxy, a gateway or the wrong
		// address - not the service. Report what arrived rather than a decode
		// error nobody can act on.
		return nil, fmt.Errorf("earnwallet: %s answered %s with a body that is not an envelope: %s",
			path, resp.Status, truncate(payload))
	}
	if decoded.Code != 0 {
		return nil, &APIError{Code: decoded.Code, Message: decoded.Message, StatusCode: resp.StatusCode}
	}
	// A non-2xx that still decoded as a success envelope is a contradiction;
	// treating it as success would credit an outcome the service did not report.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Code: decoded.Code, Message: resp.Status, StatusCode: resp.StatusCode}
	}
	return &decoded.Data, nil
}

func truncate(payload []byte) string {
	const limit = 256
	if len(payload) <= limit {
		return string(payload)
	}
	return string(payload[:limit]) + "..."
}
