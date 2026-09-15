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

Every callback carries `event` and `event_id`. Dispatch on `event`
(`deposit.confirmed`, `withdrawal.succeeded`, `withdrawal.failed`) rather than
on which URL it arrived at, and dedupe on `event_id` — it is stable across
redeliveries and the reconciliation endpoint reports the same value, so
catching up after an outage is a set difference rather than re-derived keys.

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
arrive again if the response was lost. Dedupe on `EventID` — every event has one, it is stable across
redeliveries, and the reconciliation endpoints report the same value.

Do **not** dedupe deposits on `TxHash`: one transaction can pay several deposit
addresses, so the hash is not unique. `EventID` carries the transfer index too.

**Answer 2xx only when you are done.** Anything else is retried with backoff for
about a day, after which the event is parked for an operator and can be replayed
by hand. Return an error from your function when a retry could genuinely
succeed — a database that is temporarily down. Do not return one for an event
you have permanently rejected: that buys a day of pointless retries and a dead
letter someone has to look at.

**Times are unix seconds.** Both `BlockTime` and `Timestamp`, one unit
throughout. `Timestamp` is written once when the deposit is credited and
replayed unchanged, so it does not drift across retries.

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

Every route is `POST /v1/<resource>/<action>` with a JSON body, and every one
of them needs the tenant key.

```go
client, err := earnwallet.NewClient("https://earnwallet.internal",
    earnwallet.WithAPIKey(os.Getenv("EARNWALLET_API_KEY")))
if err != nil {
    return err
}

// One endpoint answers three questions; these name them.
wallet, err := client.TakeWallet(ctx, "bsc")                  // one nobody has used
wallet, err = client.WalletBySeed(ctx, "bsc", 1001)           // which address is this seed's
wallet, err = client.WalletByAddress(ctx, "bsc", "0xabc...")  // whose address is this

order, err := client.CreateWithdrawal(ctx, earnwallet.CreateWithdrawalReq{
    ExternalId: "wd-0001",
    Chain:      "bsc",
    TokenId:    "0x55d398326f99059ff775485246999027b3197955",
    ToAddress:  "0x...",
    Amount:     "100",
})
```

Nothing is created on chain by `GetWallet`. The address is computed from the
factory and the seed, and the wallet contract is not deployed until the first
collection sweeps it — funds arrive either way. Do not wait for a confirmation;
there is no transaction.

The seed identifies that wallet on **every** chain, so one seed plus a chain
name is all that is ever needed to get an address back.

Every response travels in a `{code, message, data}` envelope, and a business
failure arrives as **HTTP 200 with a non-zero code** rather than as an error
status. The client unwraps that: `data` is returned, and a non-zero code comes
back as `*APIError`.

```go
var apiErr *earnwallet.APIError
if errors.As(err, &apiErr) {
    // The service rejected it. Do not retry as-is.
}
```

Anything else is a transport failure, and it is not a rejection: a timed-out
submission may still have created the order. Retry with the same `ExternalId` —
that is what makes the retry safe — rather than opening a second one.

`GetWallet` has no such key. A timed-out call retried without a seed takes a
second seed and abandons the first, which costs nothing because nobody was ever
told about it — but do not store both.

Ask `ListChains` what this deployment supports rather than hardcoding chain
names, token addresses or limits: they are configuration and differ between
environments. A token reported with `withdrawable: false` has no configured
ceiling, and on an outbound path that means not allowed, never unbounded.

`ListDeposits` and `ListWithdrawals` exist for recovery, not as a second
delivery path: a receiver that was down long enough for its events to
dead-letter can catch up by itself instead of asking the custodian's operator
to replay them.

## How this is built

`openapi.yaml` is exported by the service; nothing in it is edited here.

```
chainwallet openapi --out openapi.yaml   # in the service repository
go generate ./...
```

Generation covers the types **and one method per operation**, so adding a route
to the service adds a method here without anyone writing one. What is not
generated is the part that does not grow with the API: the transport, the
envelope, and what counts as a failure, all in `client.go`.

The client is emitted from `templates/client.tmpl` rather than oapi-codegen's
stock template, which imports `oapi-codegen/runtime` to encode query strings and
pulls two more modules in behind it. Four scalar parameters are not worth a
dependency in a package other teams import.

The template is deliberately dumb — it calls `formatParam`, `replacePathParam`
and `send`, all hand-written and tested. Judgement lives in reviewed code;
templates only repeat it per route.

Two invariants from the exported specification hold it up, and both are
asserted in the service's own tests rather than assumed:

- every operation has an `operationId`
- `operationId` is the response type minus its `Res` suffix, so
  `{{.OperationId}}Res` always names a real generated type

`TestEveryPublishedOperationHasAMethod` fails if an exported operation has no
method, and `client.gen.go` is checked in, as is usual for Go: consumers must
not need a code generator to build.

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

None. The whole package is standard library, so nothing here can widen your
dependency graph or your audit surface.

The code generator is a build-time tool pinned in `generate.go`; it is invoked
with `go run <pkg>@<version>`, which never enters `go.mod`.
