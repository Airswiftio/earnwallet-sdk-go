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
	"time"
)

// This file is the half of the client that does not grow with the API: the
// transport, the envelope, and what counts as a failure. One method per
// operation is generated onto Client from templates/client.tmpl, so adding a
// route to the service adds a method here without anyone writing one.
//
// The split is deliberate. Generated code scales; reviewed code is where
// judgement belongs. Putting the envelope rules in a template would hide the
// only part worth reading.

// Client calls the EarnWallet API.
type Client struct {
	baseURL string
	http    *http.Client
	editors []RequestEditor
}

// RequestEditor runs on every outgoing request before it is sent. It is the
// seam for whatever the deployment needs on the wire - a credential, a trace
// header, a tenant marker - so adding one later is not a breaking change to
// anything published here.
type RequestEditor func(*http.Request) error

// DefaultTimeout bounds a call when the caller supplies no client of its own.
// The zero value of http.Client is no timeout at all, and on a money path a
// stalled service would hold the caller's goroutine until the process ends.
const DefaultTimeout = 30 * time.Second

// Option configures a Client. It returns an error so a misconfiguration is
// refused by NewClient rather than surfacing on the first call - a service
// started without its credential must not report itself healthy and then fail
// on the first payout.
type Option func(*Client) error

// WithHTTPClient replaces the transport, for a timeout other than
// DefaultTimeout, a proxy, or instrumentation.
func WithHTTPClient(doer *http.Client) Option {
	return func(c *Client) error {
		if doer == nil {
			return fmt.Errorf("earnwallet: the http client is nil")
		}
		c.http = doer
		return nil
	}
}

// WithAPIKey sends the tenant credential every /v1 route requires.
//
// The key is not recoverable from the service: it stores only a hash, so this
// is the copy that matters. Keep it out of source and out of logs - anything
// that can read it can submit a payout.
func WithAPIKey(key string) Option {
	return func(c *Client) error {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("earnwallet: the api key is empty")
		}
		c.editors = append(c.editors, func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+key)
			return nil
		})
		return nil
	}
}

// WithRequestEditor appends an editor. Editors run in the order they were added.
func WithRequestEditor(edit RequestEditor) Option {
	return func(c *Client) error {
		if edit == nil {
			return fmt.Errorf("earnwallet: the request editor is nil")
		}
		c.editors = append(c.editors, edit)
		return nil
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

	client := &Client{baseURL: trimmed, http: &http.Client{Timeout: DefaultTimeout}}
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("earnwallet: a nil option was given")
		}
		if err = opt(client); err != nil {
			return nil, err
		}
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

// formatParam renders a scalar for a query string. The generated methods call
// it rather than carrying a type switch each, and the switch lives here so a
// parameter type the API has never used cannot be silently stringified by a
// template nobody reads.
func formatParam(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		return fmt.Sprint(typed)
	}
}

// replacePathParam substitutes one {name} placeholder, escaped so a value
// containing a slash lands as one path segment instead of walking out of the
// route it was meant to identify.
//
// A blank value is left unsubstituted on purpose, so checkPath reports which
// parameter was missing. Escaping it instead would send "%20" as an id and get
// back a not-found that says nothing about the real mistake.
func replacePathParam(path, name, value string) string {
	if strings.TrimSpace(value) == "" {
		return path
	}
	return strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
}

// envelope is what every endpoint answers with. Decoding it once here is why
// the generated methods can return the payload directly.
//
// Code is a pointer because zero is the success value: read into an int, a body
// with no code field at all is indistinguishable from one reporting success.
// Anything between the caller and the service can answer with JSON of its own -
// a gateway reporting a rate limit, a proxy reporting no upstream - and that
// body would otherwise decode into an empty payload and be returned as a result.
type envelope[T any] struct {
	Code    *int   `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func send[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any) (*T, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}

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

	// A body that is not the envelope is a proxy, a gateway or the wrong
	// address - not the service. Report what arrived rather than a decode error
	// nobody can act on, and treat a missing code the same way: valid JSON that
	// carries no verdict is not a verdict.
	var decoded envelope[T]
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Code == nil {
		return nil, fmt.Errorf("earnwallet: %s answered %s with a body that is not an envelope: %s",
			path, resp.Status, truncate(payload))
	}
	if *decoded.Code != 0 {
		return nil, &APIError{Code: *decoded.Code, Message: decoded.Message, StatusCode: resp.StatusCode}
	}
	// A non-2xx that still decoded as a success envelope is a contradiction;
	// treating it as success would report an outcome the service never gave.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Code: *decoded.Code, Message: resp.Status, StatusCode: resp.StatusCode}
	}
	return &decoded.Data, nil
}

// checkPath refuses a route that did not come out whole.
//
// An empty path parameter leaves an empty segment, which addresses the
// collection rather than the item - GET /v1/withdrawals/ is a different request
// from GET /v1/withdrawals/{id}, not a failing one, so it has to be caught
// before it is sent. A surviving brace means a placeholder was never
// substituted at all.
func checkPath(path string) error {
	if open := strings.Index(path, "{"); open >= 0 {
		name := path[open+1:]
		if close := strings.Index(name, "}"); close >= 0 {
			name = name[:close]
		}
		return fmt.Errorf("earnwallet: path parameter %q is empty or missing", name)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "" {
			return fmt.Errorf("earnwallet: a path parameter was empty, which would address %s", path)
		}
	}
	return nil
}

func truncate(payload []byte) string {
	const limit = 256
	if len(payload) <= limit {
		return string(payload)
	}
	return string(payload[:limit]) + "..."
}
