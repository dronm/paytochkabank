package paytochkabank

import (
	"errors"
	"fmt"
)

// ErrUnsupportedPaymentMethod means this payment rail cannot be handled by the
// requested operation; in particular digital-ruble refunds require the bank UI.
var ErrUnsupportedPaymentMethod = errors.New("paytochkabank: unsupported payment method")

// ValidationError means that no request was submitted because input was invalid.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("paytochkabank: %s: %s", e.Field, e.Message)
}

// APIError represents a non-2xx bank response. Body is retained for diagnostics
// and may contain sensitive customer data; do not log it indiscriminately.
type APIError struct {
	StatusCode     int
	Code           string
	Message        string
	Body           []byte
	OutcomeUnknown bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("paytochkabank: bank returned HTTP %d (%s): %s", e.StatusCode, e.Code, e.Message)
}

// RequestError wraps transport, body-read, and successful-response decoding
// errors. When OutcomeUnknown is true, query bank state before repeating a write.
type RequestError struct {
	Method         string
	Path           string
	Err            error
	OutcomeUnknown bool
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("paytochkabank: %s %s: %v", e.Method, e.Path, e.Err)
}

func (e *RequestError) Unwrap() error { return e.Err }

// IsOutcomeUnknown reports whether a submitted write might have been applied.
// It is deliberately conservative: a timeout does not prove a bank rejection.
func IsOutcomeUnknown(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.OutcomeUnknown
	}
	var requestErr *RequestError
	return errors.As(err, &requestErr) && requestErr.OutcomeUnknown
}
