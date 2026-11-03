package paytochkabank_test

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dronm/paytochkabank"
)

func ExampleParseMoney() {
	amount, err := paytochkabank.ParseMoney("123.45")
	if err != nil {
		panic(err)
	}
	fmt.Println(int64(amount), amount.Rubles())
	// Output: 12345 123.45
}

// This example is compiled, but not run: it needs your company's live token.
func ExampleAcquiringService_CreateCardCheckout() {
	client, err := paytochkabank.NewClient(os.Getenv("TOCHKA_JWT"))
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	checkout, err := client.Acquiring.CreateCardCheckout(ctx, paytochkabank.CreateCardCheckoutRequest{
		CustomerCode:  "300000092",
		MerchantID:    "200000000001056",
		Amount:        paytochkabank.Money(12345),
		Purpose:       "Order 123",
		PaymentLinkID: "order-123-attempt-1",
		RedirectURL:   "https://shop.example/payment/result",
		TTLMinutes:    30,
	})
	if err != nil {
		// If IsOutcomeUnknown(err), reconcile before submitting another attempt.
		panic(err)
	}
	// Store OperationID and return PaymentLink to the frontend.
	fmt.Println(checkout.OperationID, checkout.PaymentLink)
}

func ExampleSBPService_CreateDynamicQR() {
	client, err := paytochkabank.NewClient(os.Getenv("TOCHKA_JWT"))
	if err != nil {
		panic(err)
	}
	qr, err := client.SBP.CreateDynamicQR(context.Background(), paytochkabank.CreateDynamicQRRequest{
		MerchantID:     "MF0000000001",
		AccountID:      "40817810802000000008/044525104",
		Amount:         paytochkabank.Money(12345),
		PaymentPurpose: "Order 123",
		TTLMinutes:     15,
		ImageParams: &paytochkabank.QRImageParams{
			Width:     300,
			Height:    300,
			MediaType: "image/png",
		},
	})
	if err != nil {
		panic(err)
	}
	// Store QRCID and display the bank-supplied image or payment URL.
	fmt.Println(qr.QRCID, qr.Payload)
}

func ExampleAcquiringService_Refund() {
	client, err := paytochkabank.NewClient(os.Getenv("TOCHKA_JWT"))
	if err != nil {
		panic(err)
	}
	// A full refund uses the entire remaining refundable amount instead.
	result, err := client.Acquiring.Refund(context.Background(), "stored-operation-id", paytochkabank.RefundAcquiringRequest{
		Amount: paytochkabank.Money(2500),
	})
	if err != nil {
		// A non-nil result on error contains diagnostics, not proof of acceptance.
		panic(err)
	}
	fmt.Println(result.IsRefund)
	// Track the payment and its refund records with Acquiring.GetPayment.
}

func ExampleSBPService_Refund() {
	client, err := paytochkabank.NewClient(os.Getenv("TOCHKA_JWT"))
	if err != nil {
		panic(err)
	}
	refund, err := client.SBP.Refund(context.Background(), paytochkabank.RefundSBPRequest{
		BankCode:         "044525104",
		AccountCode:      "40817810802000000008",
		QRCID:            "stored-qrc-id",
		RefTransactionID: "stored-ref-transaction-id",
		Amount:           paytochkabank.Money(2500),
	})
	if err != nil {
		panic(err)
	}
	// Store RequestID. Reconcile later using application-managed scheduling.
	status, err := client.SBP.GetRefund(context.Background(), refund.RequestID)
	if err != nil {
		panic(err)
	}
	fmt.Println(status.Status, status.Completed(), status.Final())
}

func ExampleWebhookVerifier_Verify() {
	// Fetch this key from WebhookPublicKeyURL using trusted configuration.
	publicKeyBytes, err := os.ReadFile("tochka-public-key.json")
	if err != nil {
		panic(err)
	}
	key, err := paytochkabank.ParseWebhookPublicKey(publicKeyBytes)
	if err != nil {
		panic(err)
	}
	verifier, err := paytochkabank.NewWebhookVerifier(key)
	if err != nil {
		panic(err)
	}
	// In your HTTP handler this is the bounded, raw text/plain request body.
	rawJWT := []byte("the.raw.jwt")
	event, err := verifier.Verify(rawJWT)
	if err != nil {
		panic(err)
	}
	if event.WebhookType == paytochkabank.WebhookIncomingSBPPayment {
		payment, err := event.SBPPayment()
		if err != nil {
			panic(err)
		}
		// Match stored company, merchant, QRCID and amount before updating state.
		fmt.Println(payment.QRCID, payment.RefTransactionID, payment.Amount.Rubles())
	}
}
