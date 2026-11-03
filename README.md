# paytochkabank

A small Go client for Tochka Bank card checkout, dynamic СБП QR payments, and full or partial refunds.

**Version 0.1.0 is a draft.** It has local HTTP and signature tests, but has not been verified against a live merchant account. The acquiring-refund response and QR-status response require additional contract verification; see [API notes](docs/api-notes.md).

Requires Go 1.22 or newer. Uses only the Go standard library.

## Scope

| Service | Operations |
| --- | --- |
| `client.Acquiring` | Create a card-only hosted checkout, retrieve payment state, request a refund |
| `client.SBP` | Create a single-use dynamic QR, retrieve QR status and historical payments, request and track a refund |
| `WebhookVerifier` | Verify raw RS256 JWT notifications and decode acquiring or СБП events |

Fiscal receipts, idempotency, database models, HTTP handlers, order transitions, refund permissions, and reconciliation scheduling belong to the application. The package does not collect card details, poll automatically, or retry financial writes.

## Use the draft locally

The intended module path is `github.com/dronm/paytochkabank`. Until you publish it, extract this directory next to your application and add a local replacement to your application's `go.mod`:

```go
require github.com/dronm/paytochkabank v0.0.0

replace github.com/dronm/paytochkabank => ../paytochkabank
```

Import it as:

```go
import "github.com/dronm/paytochkabank"
```

## Configure the client

For your own company, generate a JWT key in Tochka internet banking. Pass the raw token, without the `Bearer ` prefix. Keep it on your backend. JWT keys have no refresh token; replace the configured token when the bank key is reissued.

```go
client, err := paytochkabank.NewClient(
	os.Getenv("TOCHKA_JWT"),
	paytochkabank.WithHTTPClient(&http.Client{Timeout: 20 * time.Second}),
)
if err != nil {
	return err
}
```

Default base URL: `https://enter.tochka.com/uapi`. The default HTTP timeout is 30 seconds and the response-body limit is 2 MiB. `WithBaseURL`, `WithHTTPClient`, and `WithResponseLimit` customize those settings. HTTP is permitted only for localhost test servers; use HTTPS for remote endpoints.

The client disables redirects without modifying the caller's HTTP client. Custom transports must not retry writes or expose authorization headers. A configured client and its services can be shared across goroutines; do not reassign service fields while it is in use.

This draft expects you to configure identifiers obtained during bank setup:

- Acquiring: `CustomerCode` and the internet-acquiring `MerchantID`.
- Direct СБП: the СБП `MerchantID`, and `AccountID` in `account-number/BIC` form.
- СБП refunds: `BankCode` and `AccountCode` for your company's account.

**Acquiring and СБП merchant IDs are different identifiers.** Internet acquiring must be enabled, and the company and merchant must be registered for direct СБП. Merchant-registration and account-discovery APIs are outside this draft.

## Exact monetary amounts

Every exported `Money` value is an `int64` number of kopecks:

```go
amount := paytochkabank.Money(12345) // 123.45 RUB
amount, err := paytochkabank.ParseMoney("123.45")
if err != nil {
	return err
}
```

`ParseMoney` does not round; it rejects overflow and nonzero fractions smaller than one kopeck. `amount.Rubles()` returns a decimal string such as `"123.45"`. Public JSON models encode `Money` as integer kopecks. The package converts internally to the bank's decimal-ruble number, decimal-ruble string, or integer-kopek wire format as required by each method. Avoid converting prices through `float64`.

## Card checkout

```go
checkout, err := client.Acquiring.CreateCardCheckout(ctx, paytochkabank.CreateCardCheckoutRequest{
	CustomerCode:  customerCode,
	MerchantID:    acquiringMerchantID,
	Amount:        paytochkabank.Money(12345),
	Purpose:       "Order 123",
	PaymentLinkID: "order-123-attempt-1",
	RedirectURL:   "https://shop.example/payment/result",
	TTLMinutes:    30,
})
if err != nil {
	return err
}
```

Store `checkout.OperationID` and send `checkout.PaymentLink` to your frontend. This method always selects `paymentMode: ["card"]` and ordinary one-stage payment. A `PaymentLinkID` identifies your payment attempt; its uniqueness does not establish safe retry semantics.

Retrieve the current result with `client.Acquiring.GetPayment(ctx, operationID)`. The bank's native status is retained, including unknown future values. `APPROVED` means the payment succeeded; `AUTHORIZED` represents a hold and is not a completed ordinary payment. Refund records are available in `payment.Orders`.

## Dynamic СБП QR

```go
qr, err := client.SBP.CreateDynamicQR(ctx, paytochkabank.CreateDynamicQRRequest{
	MerchantID:     sbpMerchantID,
	AccountID:      accountID,
	Amount:         paytochkabank.Money(12345),
	PaymentPurpose: "Order 123",
	TTLMinutes:     15,
	RedirectURL:    "https://shop.example/payment/result",
	ImageParams: &paytochkabank.QRImageParams{
		Width: 300, Height: 300, MediaType: "image/png",
	},
})
if err != nil {
	return err
}
```

Store `qr.QRCID`. `qr.Payload` is a payment URL for opening the customer's banking app. If `qr.Image` is present, `qr.Image.Bytes()` decodes the PNG or returns the SVG bytes. Treat SVG as untrusted markup when presenting it.

The request always creates a dynamic, single-use code (`qrcType: "02"`) with a fixed amount. A zero `TTLMinutes` leaves the bank's default unchanged. Supplied image dimensions must be 200–2000 pixels.

`client.SBP.GetQRPaymentStatus(ctx, qrcID)` preserves the raw `Data` JSON because the response schema needs verification. `client.SBP.GetPayments(ctx, GetSBPPaymentsRequest{...})` provides documented typed states and `RefTransactionID`, filtered by `QRCID` if required. Supply `FromDate` when reconciling older payments: the bank's default period is only yesterday and today. Direct QR-status queries have a limited lookup window; persist successful payment data promptly.

## Full and partial refunds

Both refund methods require a positive amount. For a full refund, pass the full remaining refundable amount. For a partial refund, pass the smaller amount. The application calculates the remaining amount and the bank validates eligibility; the client does not query state automatically before submitting a refund.

```go
result, err := client.Acquiring.Refund(ctx, operationID, paytochkabank.RefundAcquiringRequest{
	Amount: paytochkabank.Money(2500), // 25.00 RUB
})
```

Acquiring refunds use the original checkout's `OperationID`. The bank documents `APPROVED` as the eligible status. Inspect the returned optional fields and `RawData`, then reconcile through `GetPayment`. An unfamiliar response can return a non-nil diagnostic result alongside an error; its `RawData` does not imply acceptance. A successful HTTP request does not establish when the buyer receives the funds. This draft's acquiring-refund response model is provisional.

```go
refund, err := client.SBP.Refund(ctx, paytochkabank.RefundSBPRequest{
	BankCode:         bankCode,
	AccountCode:      accountCode,
	QRCID:            qrcID,
	RefTransactionID: refTransactionID,
	Amount:           paytochkabank.Money(2500),
})
```

For СБП, supply exactly one of `RefTransactionID` and `TrxID`, plus the original `QRCID`. Save `refund.RequestID`, then call `client.SBP.GetRefund(ctx, requestID)` from application-managed reconciliation. `refund.Completed()` is true only for `Accepted`; `refund.Final()` is true for `Accepted` or `Rejected`. Other and unknown states remain pending.

API refunds are available for direct dynamic СБП QR payments. Digital-ruble payments require a manual refund in internet banking. A known digital-ruble `TrxID` is rejected locally; when refunding by `RefTransactionID`, your application must establish the payment method. Refunds to nonresidents have additional bank requirements for `Purpose`; consult the linked bank guide.

## Webhook verification

Configure the bank's webhook subscription separately for `acquiringInternetPayment` and `incomingSbpPayment`. Tochka sends a raw JWT with `Content-Type: text/plain`, rather than a JSON envelope. Its callback endpoint must use HTTPS on port 443.

Fetch Tochka's public key from `paytochkabank.WebhookPublicKeyURL` through trusted application configuration and refresh it when needed. The package parses a single public RSA JWK or PEM public key; it never trusts a key URL supplied by a webhook.

```go
key, err := paytochkabank.ParseWebhookPublicKey(publicKeyBytes)
if err != nil {
	return err
}
verifier, err := paytochkabank.NewWebhookVerifier(key)
if err != nil {
	return err
}
event, err := verifier.Verify(rawJWT)
if err != nil {
	return err
}
```

Use `event.AcquiringPayment()`, `event.SBPPayment()`, or `event.Decode(&yourPayload)` after verification. The original verified payload is retained. The verifier accepts only RS256, validates `exp` and `nbf` when present, and does not provide replay protection.

Your handler must still check company, merchant, payment identifier, amount, and order state. Persist the result and handle duplicate notifications at application level. Do not mark an order paid from a browser redirect. Missing notifications do not prove failure; use status queries. Webhook registration, key-fetch scheduling, and HTTP handlers are outside the package.

## Error handling and uncertain outcomes

Use `errors.As` to inspect `*ValidationError`, `*APIError`, and `*RequestError`. `RequestError` unwraps its underlying error, so context cancellation and deadlines remain inspectable.

```go
if err != nil {
	if paytochkabank.IsOutcomeUnknown(err) {
		// Persist the attempt and reconcile bank state before another write.
		return err
	}
	var apiErr *paytochkabank.APIError
	if errors.As(err, &apiErr) {
		// Inspect StatusCode, Code, and Message without logging customer data.
	}
	return err
}
```

An uncertain outcome means a submitted write might have been applied. Transport errors, malformed successful write responses, and selected server/dependency errors are treated conservatively. This signal is not an idempotency mechanism. Even a known HTTP error should not be retried without an application policy. Error bodies and raw responses may contain sensitive data.

## Sandbox and verification

```go
client, err := paytochkabank.NewClient(
	paytochkabank.SandboxToken,
	paytochkabank.WithBaseURL(paytochkabank.SandboxURL),
)
```

The bank sandbox returns fixed examples independently of supplied values. It cannot complete checkout or simulate a payment/refund lifecycle. Local tests verify request formats, response handling, validation, exact money conversion, and signature verification. They do not certify bank-side compatibility.

Run the package checks with:

```sh
go test ./...
go test -race ./...
go vet ./...
```

See [examples_test.go](examples_test.go) for complete, compilable usage examples. Payment examples are compiled but never executed by `go test`; they contain no real credentials.

Before depending on the draft for real payments, verify the provisional contracts and run a small, controlled payment and refund through your enabled merchant account. See [API notes](docs/api-notes.md) for endpoint mappings, source links, and remaining verification work.
