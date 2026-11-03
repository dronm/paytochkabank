// Package paytochkabank implements the Tochka Bank payment-link and dynamic SBP
// QR APIs. All exported monetary amounts are integer kopecks represented by Money.
//
// The client performs one HTTP attempt per call and does not follow redirects.
// Order persistence, fiscal receipts, idempotency, and reconciliation policies
// belong to the application. A successful request is not necessarily a completed
// payment or refund: inspect the returned bank status and keep bank identifiers.
package paytochkabank
