package earnwallet_test

import (
	"context"
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
				return settleFailure(r.Context(), event.ThirdPartyID, event.FailedReason)
			}
			return settleSuccess(r.Context(), event.ThirdPartyID, event.TxID)
		}))
}

func ExampleClientWithResponses_CreateWithdrawalWithResponse() {
	client, err := earnwallet.NewClientWithResponses("https://earnwallet.internal")
	if err != nil {
		panic(err)
	}

	res, err := client.CreateWithdrawalWithResponse(context.Background(), earnwallet.CreateWithdrawalReq{
		ExternalId: "wd-0001",
		Chain:      "bsc",
		TokenId:    "0x55d398326f99059ff775485246999027b3197955",
		ToAddress:  "0x0000000000000000000000000000000000000001",
		Amount:     "100",
	})
	// A transport error is not a rejection: the order may still exist. Retry
	// with the same ExternalId rather than opening a second one.
	if err != nil {
		panic(err)
	}
	if res.JSON200 == nil {
		panic(fmt.Sprintf("unexpected reply: %s", res.Body))
	}
	// A business failure arrives as HTTP 200 with a non-zero code.
	if res.JSON200.Code != 0 {
		panic(fmt.Sprintf("%d %s", res.JSON200.Code, res.JSON200.Message))
	}
	fmt.Println(res.JSON200.Data.Status)
}

func credit(context.Context, string, string, string) error { return nil }
func settleFailure(context.Context, string, string) error  { return nil }
func settleSuccess(context.Context, string, string) error  { return nil }
