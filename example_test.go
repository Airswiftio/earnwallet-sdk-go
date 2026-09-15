package earnwallet_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	earnwallet "github.com/Airswiftio/earnwallet-sdk-go"
)

// These compile against the real package, so a README snippet cannot drift from
// the generated API without the build noticing.

func ExampleDepositHandler() {
	verifier := earnwallet.Verifier{
		Keys: []earnwallet.Key{{ID: "current", Secret: os.Getenv("EARNWALLET_CALLBACK_SECRET")}},
	}

	mux := http.NewServeMux()
	mux.Handle("/callbacks/earnwallet/deposit", earnwallet.DepositHandler(verifier,
		func(r *http.Request, event earnwallet.DepositEvent) error {
			// Key on TxID: delivery is at-least-once, so this runs again if the
			// reply is lost.
			return credit(r.Context(), event.TxID, event.Address, event.Amount)
		}))

	mux.Handle("/callbacks/earnwallet/withdraw", earnwallet.WithdrawalHandler(verifier,
		func(r *http.Request, event earnwallet.WithdrawalEvent) error {
			if !event.Succeeded() {
				return settleFailure(r.Context(), event.ExternalID, event.FailedReason)
			}
			return settleSuccess(r.Context(), event.ExternalID, event.TxID)
		}))
}

func ExampleClient_CreateWithdrawal() {
	client, err := earnwallet.NewClient("https://earnwallet.internal")
	if err != nil {
		panic(err)
	}

	order, err := client.CreateWithdrawal(context.Background(), earnwallet.CreateWithdrawalJSONRequestBody{
		ExternalId: "wd-0001",
		Chain:      "bsc",
		TokenId:    "0x55d398326f99059ff775485246999027b3197955",
		ToAddress:  "0x0000000000000000000000000000000000000001",
		Amount:     "100",
	})

	// A business failure arrives as HTTP 200 with a non-zero code, and comes
	// back as *APIError rather than as a status to inspect.
	var apiErr *earnwallet.APIError
	if errors.As(err, &apiErr) {
		panic(fmt.Sprintf("rejected: %d %s", apiErr.Code, apiErr.Message))
	}
	// Anything else is a transport failure, and the order may still exist.
	// Retry with the same ExternalId rather than opening a second one.
	if err != nil {
		panic(err)
	}

	fmt.Println(order.Status)
}

func credit(context.Context, string, string, string) error { return nil }
func settleFailure(context.Context, string, string) error  { return nil }
func settleSuccess(context.Context, string, string) error  { return nil }
