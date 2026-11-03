# Changelog

## 0.1.0 — draft — 2026-11-02

- Added ordinary card-only hosted checkout and payment-status retrieval.
- Added fixed-amount dynamic СБП QR creation, QR display data, raw QR-status retrieval, and historical payment queries.
- Added explicit-amount full and partial acquiring and direct СБП refunds, with СБП refund-status retrieval.
- Added exact integer-kopek money handling with bank-specific wire conversions.
- Added context-aware HTTP requests, configurable transport and response limits, typed errors, and uncertain-outcome reporting.
- Added RS256 webhook verification, trusted RSA key parsing, and typed acquiring and СБП payload helpers.
- Added local contract and signature tests. No live merchant-account verification has been performed.

Acquiring-refund and QR-status response contracts remain provisional. Fiscalization, application idempotency, persistence, order policies, and automatic retries are outside the package.
