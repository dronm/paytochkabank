# API contracts and draft limitations

This draft targets **Точка.API → API банка**, using `https://enter.tochka.com/uapi`. It does not implement the separate Pay Gateway product or take card details directly.

Documentation was reviewed on 2026-11-02. The implementation uses local HTTP fixtures and signature tests. No JWT belonging to a merchant was supplied, and no live payment or refund was performed.

## Endpoint mapping

Paths below are relative to the configured base URL.

| Package method | HTTP endpoint | Amount sent to bank |
| --- | --- | --- |
| `Acquiring.CreateCardCheckout` | `POST /acquiring/v1.0/payments` | Decimal-ruble JSON number |
| `Acquiring.GetPayment` | `GET /acquiring/v1.0/payments/{operationId}` | — |
| `Acquiring.Refund` | `POST /acquiring/v1.0/payments/{operationId}/refund` | Decimal-ruble JSON number |
| `SBP.CreateDynamicQR` | `POST /sbp/v1.0/qr-code/merchant/{merchantId}/{accountId}` | Integer kopecks |
| `SBP.GetQRPaymentStatus` | `GET /sbp/v1.0/qr-codes/{qrcIds}/payment-status` | — |
| `SBP.GetPayments` | `GET /sbp/v1.0/get-sbp-payments` | — |
| `SBP.Refund` | `POST /sbp/v1.0/refund` | Decimal-ruble string |
| `SBP.GetRefund` | `GET /sbp/v1.0/refund/{request_id}` | — |

`GetQRPaymentStatus` currently accepts one QR ID. The endpoint reference uses the plural placeholder `qrcIds`; batch request syntax is intentionally not assumed. `accountId` has the documented `account-number/BIC` form.

## Provisional contracts

The official acquiring-refund and QR-status method pages were discoverable, but their complete request/response schemas could not be retrieved in this environment. These are explicit limitations of the draft:

- **Acquiring refund:** the implementation sends the requested amount under `Data.amount`. It retains the complete successful `Data` payload and decodes optional result fields, including `isRefund`. Do not infer completed settlement from an absent field or an HTTP 200. Compare real responses against the current bank specification and reconcile through `GetPayment`.
- **QR payment status:** the implementation preserves `Data` as raw JSON instead of inventing a response structure. Typed payment states and refund identifiers are available through the documented `GetPayments` endpoint and verified СБП webhooks. Verify the direct status endpoint against a real or bank-provided response before adding a typed decoder.

Unknown status strings are preserved. An unfamiliar status is not automatically treated as successful or final. Tests containing representative JSON are local fixtures, not recordings from a merchant account.

## Payment and refund semantics

- Card checkout always uses `paymentMode: ["card"]`, with ordinary one-stage collection. Capture, hold cancellation, saved cards, and subscriptions are outside the draft.
- Dynamic QR creation always uses `qrcType: "02"`. Static, cashbox, and B2B QR workflows are outside the draft.
- Both refund APIs take an explicit positive amount. The application determines whether it represents the full remaining balance or a partial refund.
- Acquiring documentation identifies `APPROVED` as the eligible payment state. The refund method does not perform a hidden preflight query or validate the application's ledger.
- Direct СБП refund requires `qrcId` plus either `refTransactionId` or `trxId`. The webhook's `operationId` is the NSPK `trxId` for an ordinary СБП payment.
- СБП refund requests can remain pending after a successful HTTP response. `Accepted` is final success and `Rejected` is final failure.
- API refunds for digital-ruble payments are unavailable in the documented flows; use internet banking. The application must inspect the payment rail before choosing an API refund path.
- Direct СБП QR payments are not fiscalized by this package or by the direct QR API. Integrate receipt issuance at application level as appropriate.

## Application responsibilities

Persist bank identifiers, amount, company/merchant correlation, payment attempts, and refund attempts. Verify webhook signatures before using payloads, then check them against stored state. Handle replay and duplicate delivery in the application. A redirect is only a navigation event.

The client sends one request attempt and refuses HTTP redirects. On an unknown write outcome, reconcile before submitting another write. A unique `paymentLinkId` is not assumed to be a general bank idempotency key. Custom transports must retain these guarantees.

The raw bank error body, raw refund data, and webhook payload can contain customer information. Keep them out of routine logs unless the application's data-handling policy explicitly permits it.

## Authentication and onboarding

For a single company's integration, the bank recommends a JWT bearer key created in internet banking. JWT keys have no refresh token. OAuth onboarding for other merchants is outside this draft; callers can construct a replacement client when credentials change.

Configure the account and merchant identifiers explicitly. Acquiring and direct СБП use different merchant identifiers. Internet acquiring and direct СБП registration must be enabled with the bank before production calls.

Relevant permissions are `MakeAcquiringOperation`, `ReadAcquiringData`, `ReadCustomerData`, `EditSBPData`, and `ReadSBPData`. Webhook registration also requires `ManageWebhookData`; registration itself is performed outside this package.

## Sandbox limits

The public sandbox base is `https://enter.tochka.com/sandbox/v2`, with the documented `sandbox.jwt.token`. Its fixed responses validate shapes, not business workflows. Generated payment links cannot complete checkout. Do not treat a sandbox result or a unit-test fixture as proof of successful live integration.

## Official references

- [Internet-acquiring integration](https://developers.tochka.com/docs/tochka-api/internet-acquiring-integration)
- [JWT authorization](https://developers.tochka.com/docs/tochka-api/algoritm-raboty-s-jwt-tokenom)
- [Payment-link workflow and refunds](https://developers.tochka.com/docs/tochka-api/opisanie-metodov/platyozhnye-ssylki)
- [Create Payment Operation](https://developers.tochka.com/docs/tochka-api/api/create-payment-operation-acquiring-v-1-0-payments-post)
- [Get Payment Operation Info](https://developers.tochka.com/docs/tochka-api/api/get-payment-operation-info-acquiring-v-1-0-payments-operation-id-get)
- [Refund Payment Operation](https://developers.tochka.com/docs/tochka-api/api/refund-payment-operation-acquiring-v-1-0-payments-operation-id-refund-post)
- [Dynamic QR workflow](https://developers.tochka.com/docs/tochka-api/opisanie-metodov/sbp-sistema-bystryh-platezhej/rabota-s-qr-kodami)
- [Register Qr Code](https://developers.tochka.com/docs/tochka-api/api/register-qr-code-sbp-v-1-0-qr-code-merchant-merchant-id-account-id-post)
- [Get Qr Codes Payment Status](https://developers.tochka.com/docs/tochka-api/api/get-qr-codes-payment-status-sbp-v-1-0-qr-codes-qrc-ids-payment-status-get)
- [Get Payments](https://developers.tochka.com/docs/tochka-api/api/get-payments-sbp-v-1-0-get-sbp-payments-get)
- [СБП refund workflow and restrictions](https://developers.tochka.com/docs/tochka-api/opisanie-metodov/sbp-sistema-bystryh-platezhej/rabota-s-vozvratami)
- [Start Refund](https://developers.tochka.com/docs/tochka-api/api/start-refund-sbp-v-1-0-refund-post)
- [Get Refund Data](https://developers.tochka.com/docs/tochka-api/api/get-refund-data-sbp-v-1-0-refund-request-id-get)
- [Webhook verification and delivery](https://developers.tochka.com/docs/tochka-api/opisanie-metodov/vebhuki)
- [Incoming СБП webhook](https://developers.tochka.com/docs/tochka-api/opisanie-metodov/vebhuki/incomingSbpPayment)
- [Sandbox](https://developers.tochka.com/docs/tochka-api/pesochnica)
- [Terminology and identifiers](https://developers.tochka.com/docs/tochka-api/terminologiya)
