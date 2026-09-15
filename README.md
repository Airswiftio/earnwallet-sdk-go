# earnwallet-sdk-go

Go client and callback verification for the EarnWallet API.

```
go get github.com/Airswiftio/earnwallet-sdk-go
```

The package has two halves. The API client is generated from the service's own
OpenAPI document. The callback half — signature verification and the event
types — is written by hand, because callbacks are pushed to you and no route
describes them.

## Receiving callbacks

Two endpoints, one for deposits and one for payouts. Both are signed with
HMAC-SHA256 over the raw body; the signature travels in `X-Polyflow-Signature`
and there is no signature field inside the body.

```go
verifier := earnwallet.Verifier{
    Keys: []earnwallet.Key{{ID: "current", Secret: os.Getenv("EARNWALLET_CALLBACK_SECRET")}},
}

mux.Handle("/callbacks/earnwallet/deposit", earnwallet.DepositHandler(verifier,
    func(r *http.Request, event earnwallet.DepositEvent) error {
        return credit(r.Context(), event)
    }))

mux.Handle("/callbacks/earnwallet/withdraw", earnwallet.WithdrawalHandler(verifier,
    func(r *http.Request, event earnwallet.WithdrawalEvent) error {
        return settle(r.Context(), event)
    }))
```

Four things decide whether an integration is correct:

**Verify the raw bytes.** The signature covers the body exactly as it arrived.
Decoding and re-encoding changes key order, whitespace and number formatting,
and the signature stops matching. The handlers above read the body first and
verify before decoding; if you verify by hand, do the same.

**Be idempotent.** Delivery is at-least-once. A callback that was processed will
arrive again if the response was lost. Key deposits on `DepositEvent.TxID` and
payouts on `WithdrawalEvent.ThirdPartyID`. `TxID` is `<tx hash>#<transfer index>`
rather than a bare hash, because one transaction can pay several deposit
addresses and bare hashes would collide.

**Answer 2xx only when you are done.** Anything else is retried with backoff for
about a day, after which the event is parked for an operator and can be replayed
by hand. Return an error from your function when a retry could genuinely
succeed — a database that is temporarily down. Do not return one for an event
you have permanently rejected: that buys a day of pointless retries and a dead
letter someone has to look at.

**Treat amounts as strings.** `Amount` and `RawAmount` are decimal strings. A
`float64` cannot hold an 18-decimal token amount without losing the low digits.
`Decimals` travels so you can check `Amount == RawAmount / 10^Decimals` rather
than trusting it.

### Rotating the secret

`Verifier.Keys` holds every secret currently accepted, and a body verifying
against any one of them is accepted. Hold both during a rotation and neither
side has to change at the same instant:

```go
verifier := earnwallet.Verifier{Keys: []earnwallet.Key{
    {ID: "incoming", Secret: next},
    {ID: "retiring", Secret: current},
}}
```

A `Verifier` holding no usable secret rejects everything. That is deliberate:
an endpoint that credits whoever can reach it is worse than one that is down.

## Calling the API

```go
client, err := earnwallet.NewClientWithResponses("https://earnwallet.internal")
if err != nil {
    return err
}

res, err := client.CreateWithdrawalWithResponse(ctx, earnwallet.CreateWithdrawalReq{
    ExternalId: "wd-0001",
    Chain:      "bsc",
    TokenId:    "0x55d398326f99059ff775485246999027b3197955",
    ToAddress:  "0x...",
    Amount:     "100",
})
if err != nil {
    return err              // transport failure; the order may or may not exist
}
if res.JSON200 == nil {
    return fmt.Errorf("earnwallet: unexpected reply: %s", res.Body)
}
if res.JSON200.Code != 0 {
    return fmt.Errorf("earnwallet: %d %s", res.JSON200.Code, res.JSON200.Message)
}
order := res.JSON200.Data     // nil only when code is non-zero
```

Every response is `{code, message, data}`. `code` is `0` on success; any other
value is a business failure and **arrives as HTTP 200**, so check `code` rather
than the status.

A transport error is not the same as a rejection: a timed-out submission may
still have created the order. Retry it with the same `ExternalId` — that is what
makes the retry safe — and read the result rather than opening a second order.

`ExternalId` is your idempotency key. Submitting the same one twice returns the
existing order rather than paying twice.

## Regenerating

`openapi.yaml` is produced by the service, not edited here:

```
chainwallet openapi --out openapi.yaml   # in the service repository
go generate ./...
```

The generator version is pinned in `generate.go`, so a local regeneration and
the one CI checks produce the same file.

`client.gen.go` is checked in, as is usual for Go: consumers must not need a
code generator to build.

## Conformance vectors

`testdata/signature_vectors.json` is the signing contract. The identical file
lives in the service repository, where the sending half is tested against it.
A change to the signed material, the header format or the hash therefore fails
in the commit that makes it, rather than in every integrator's verification
months later.

Regenerating the vectors to make a test pass is the wrong repair. If the scheme
must change, the header carries a new `v2` parameter alongside `v1` so receivers
can roll independently — unknown parameters are already ignored.

## Dependencies

The callback half uses only the standard library. The generated client pulls in
`github.com/oapi-codegen/runtime` for parameter binding.
