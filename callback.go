package earnwallet

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader carries the integrity check on every callback. There is no
// signature field inside the body.
const SignatureHeader = "X-Polyflow-Signature"

// DefaultTolerance bounds how far a callback's timestamp may sit from local
// time. The timestamp is covered by the signature, so a captured request cannot
// be replayed later with its timestamp moved forward.
const DefaultTolerance = 5 * time.Minute

var (
	ErrNoKeys       = errors.New("earnwallet: no verification key configured")
	ErrMalformed    = errors.New("earnwallet: malformed signature header")
	ErrMissingV1    = errors.New("earnwallet: header carries no v1 signature")
	ErrStale        = errors.New("earnwallet: timestamp outside the accepted window")
	ErrNoKeyMatched = errors.New("earnwallet: no configured key produces this signature")
)

// Key is one accepted signing secret. ID is informational; verification tries
// every key it holds.
type Key struct {
	ID     string
	Secret string
}

// Verifier authenticates callbacks.
//
// Verify the body EXACTLY as it arrived on the wire. Decoding and re-encoding
// changes bytes - key order, whitespace, number formatting - and the signature
// will not match. Read the raw body first, verify, then decode. Handler does
// this for you.
//
// Keys holds every secret currently accepted. During a secret rotation that is
// both the old and the new one; either verifying is enough, so the two sides
// never have to roll at the same instant.
type Verifier struct {
	Keys      []Key
	Tolerance time.Duration

	now func() time.Time
}

func (v Verifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

func (v Verifier) tolerance() time.Duration {
	if v.Tolerance <= 0 {
		return DefaultTolerance
	}
	return v.Tolerance
}

// Verify reports whether header authenticates body.
//
// A Verifier holding no usable key rejects everything rather than waving it
// through: a receiver that has not been given a secret yet must fail closed,
// because the alternative is an endpoint that credits whoever can reach it.
func (v Verifier) Verify(header string, body []byte) error {
	usable := make([]Key, 0, len(v.Keys))
	for _, k := range v.Keys {
		if k.Secret != "" {
			usable = append(usable, k)
		}
	}
	if len(usable) == 0 {
		return ErrNoKeys
	}

	ts, sig, err := parseSignatureHeader(header)
	if err != nil {
		return err
	}

	delta := v.clock().Sub(time.Unix(ts, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > v.tolerance() {
		return ErrStale
	}

	want, err := hex.DecodeString(sig)
	if err != nil {
		return ErrMalformed
	}

	matched := false
	for _, k := range usable {
		got, decErr := hex.DecodeString(Sign(k.Secret, ts, body))
		if decErr != nil {
			continue
		}
		// No early exit: every key is compared so the time taken reveals
		// neither which one matched nor how many are held.
		if hmac.Equal(got, want) {
			matched = true
		}
	}
	if !matched {
		return ErrNoKeyMatched
	}
	return nil
}

// Sign reproduces the signature over "<timestamp>.<body>". Binding the
// timestamp into the signed material is what makes the replay window
// enforceable: moving the timestamp invalidates the signature.
//
// Exported for tests and for receivers that verify by other means.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// parseSignatureHeader reads "t=<unix seconds>,v1=<hex>". Unknown parameters
// are ignored so a future v2 signature can travel alongside v1 without breaking
// receivers that only understand v1.
func parseSignatureHeader(header string) (ts int64, sig string, err error) {
	if header == "" {
		return 0, "", ErrMalformed
	}

	var haveTS, haveSig bool
	for _, part := range strings.Split(header, ",") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return 0, "", ErrMalformed
		}
		switch name {
		case "t":
			parsed, convErr := strconv.ParseInt(value, 10, 64)
			if convErr != nil {
				return 0, "", ErrMalformed
			}
			ts, haveTS = parsed, true
		case "v1":
			if value == "" {
				return 0, "", ErrMalformed
			}
			sig, haveSig = value, true
		}
	}
	if !haveTS {
		return 0, "", ErrMalformed
	}
	if !haveSig {
		return 0, "", ErrMissingV1
	}
	return ts, sig, nil
}
