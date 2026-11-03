package paytochkabank

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"
)

// AcquiringService operates on bank-hosted card checkout links.
// It does not accept card details, fiscalize receipts, or manage order state.
type AcquiringService struct {
	client *Client
}

// AcquiringStatus preserves the payment status reported by Tochka.
// Unknown future values are retained rather than rejected.
type AcquiringStatus string

const (
	AcquiringCreated           AcquiringStatus = "CREATED"
	AcquiringApproved          AcquiringStatus = "APPROVED"
	AcquiringOnRefund          AcquiringStatus = "ON-REFUND"
	AcquiringRefunded          AcquiringStatus = "REFUNDED"
	AcquiringExpired           AcquiringStatus = "EXPIRED"
	AcquiringRefundedPartially AcquiringStatus = "REFUNDED_PARTIALLY"
	AcquiringAuthorized        AcquiringStatus = "AUTHORIZED"
	AcquiringWaitFullPayment   AcquiringStatus = "WAIT_FULL_PAYMENT"
)

// CreateCardCheckoutRequest describes a one-stage, card-only checkout.
// Amount is in kopecks. Zero TTLMinutes leaves the bank's default unchanged.
// PaymentLinkID is the optional unique merchant order/payment-attempt identifier;
// its uniqueness does not establish an idempotent retry contract.
type CreateCardCheckoutRequest struct {
	CustomerCode    string
	MerchantID      string
	Amount          Money
	Purpose         string
	RedirectURL     string
	FailRedirectURL string
	TTLMinutes      int
	PaymentLinkID   string
}

// Checkout is a bank-hosted checkout link. Amount is in kopecks.
type Checkout struct {
	OperationID   string          `json:"operationId"`
	PaymentLink   string          `json:"paymentLink"`
	Status        AcquiringStatus `json:"status"`
	Amount        Money           `json:"amount"`
	Purpose       string          `json:"purpose"`
	MerchantID    string          `json:"merchantId,omitempty"`
	TTLMinutes    int             `json:"ttl,omitempty"`
	PaymentLinkID string          `json:"paymentLinkId,omitempty"`
	PaymentModes  []string        `json:"paymentMode,omitempty"`
}

// AcquiringOrder records one bank approval, authorization, or refund entry.
// Amount is in kopecks; Time is retained exactly as the bank returns it.
type AcquiringOrder struct {
	OrderID string `json:"orderId"`
	Type    string `json:"type"`
	Amount  Money  `json:"amount"`
	Time    string `json:"time"`
}

// AcquiringPayment contains the native identifiers needed to reconcile payments
// and refunds. CreatedAt and PaidAt retain the bank's timestamp representation.
type AcquiringPayment struct {
	Checkout
	CustomerCode  string           `json:"customerCode"`
	PaymentType   string           `json:"paymentType,omitempty"`
	PaymentID     string           `json:"paymentId,omitempty"`
	TransactionID string           `json:"transactionId,omitempty"`
	ConsumerID    string           `json:"consumerId,omitempty"`
	CreatedAt     string           `json:"createdAt,omitempty"`
	PaidAt        string           `json:"paidAt,omitempty"`
	Orders        []AcquiringOrder `json:"Order,omitempty"`
}

// RefundAcquiringRequest requests a specific amount, in kopecks.
// For a full refund supply the whole remaining refundable amount; for a partial
// refund supply a smaller amount. The bank checks eligibility and available funds.
type RefundAcquiringRequest struct {
	Amount Money
}

// AcquiringRefund retains the bank's complete refund result as RawData.
// Fields absent from the response stay unset. IsRefund is a pointer so an absent
// value cannot be mistaken for a definite rejection. Reconcile using GetPayment;
// receiving HTTP 200 alone does not prove that money reached the customer.
// Refund may return a non-nil result with an error to preserve RawData for
// diagnostics. Such a result must not be interpreted as an accepted refund.
type AcquiringRefund struct {
	IsRefund    *bool           `json:"isRefund,omitempty"`
	Amount      Money           `json:"amount"`
	OperationID string          `json:"operationId,omitempty"`
	Status      AcquiringStatus `json:"status,omitempty"`
	RawData     json.RawMessage `json:"rawData"`
}

type acquiringPaymentWire struct {
	OperationID   string          `json:"operationId"`
	PaymentLink   string          `json:"paymentLink"`
	Status        AcquiringStatus `json:"status"`
	Amount        rubleMoney      `json:"amount"`
	Purpose       string          `json:"purpose"`
	MerchantID    string          `json:"merchantId"`
	TTLMinutes    int             `json:"ttl"`
	PaymentLinkID string          `json:"paymentLinkId"`
	PaymentModes  []string        `json:"paymentMode"`
	CustomerCode  string          `json:"customerCode"`
	PaymentType   string          `json:"paymentType"`
	PaymentID     string          `json:"paymentId"`
	TransactionID string          `json:"transactionId"`
	ConsumerID    string          `json:"consumerId"`
	CreatedAt     string          `json:"createdAt"`
	PaidAt        string          `json:"paidAt"`
	Orders        []struct {
		OrderID string     `json:"orderId"`
		Type    string     `json:"type"`
		Amount  rubleMoney `json:"amount"`
		Time    string     `json:"time"`
	} `json:"Order"`
}

func (p acquiringPaymentWire) checkout() Checkout {
	return Checkout{
		OperationID: p.OperationID, PaymentLink: p.PaymentLink, Status: p.Status,
		Amount: Money(p.Amount), Purpose: p.Purpose, MerchantID: p.MerchantID,
		TTLMinutes: p.TTLMinutes, PaymentLinkID: p.PaymentLinkID, PaymentModes: p.PaymentModes,
	}
}

// CreateCardCheckout creates a one-stage hosted checkout accepting only cards.
// It never submits card details or automatically retries a failed request.
func (s *AcquiringService) CreateCardCheckout(ctx context.Context, req CreateCardCheckoutRequest) (*Checkout, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	body := dataEnvelope[struct {
		CustomerCode    string     `json:"customerCode"`
		MerchantID      string     `json:"merchantId,omitempty"`
		Amount          rubleMoney `json:"amount"`
		Purpose         string     `json:"purpose"`
		RedirectURL     string     `json:"redirectUrl,omitempty"`
		FailRedirectURL string     `json:"failRedirectUrl,omitempty"`
		PaymentModes    []string   `json:"paymentMode"`
		TTLMinutes      int        `json:"ttl,omitempty"`
		PaymentLinkID   string     `json:"paymentLinkId,omitempty"`
	}]{}
	body.Data.CustomerCode = req.CustomerCode
	body.Data.MerchantID = req.MerchantID
	body.Data.Amount = rubleMoney(req.Amount)
	body.Data.Purpose = req.Purpose
	body.Data.RedirectURL = req.RedirectURL
	body.Data.FailRedirectURL = req.FailRedirectURL
	body.Data.PaymentModes = []string{"card"}
	body.Data.TTLMinutes = req.TTLMinutes
	body.Data.PaymentLinkID = req.PaymentLinkID
	var out dataEnvelope[acquiringPaymentWire]
	if err := s.client.do(ctx, http.MethodPost, "/acquiring/v1.0/payments", nil, body, &out); err != nil {
		return nil, err
	}
	checkout := out.Data.checkout()
	if checkout.OperationID == "" || checkout.PaymentLink == "" || checkout.Amount <= 0 {
		return nil, &RequestError{Method: http.MethodPost, Path: "/acquiring/v1.0/payments", Err: fmt.Errorf("checkout response lacks operationId, paymentLink, or a positive amount"), OutcomeUnknown: true}
	}
	return &checkout, nil
}

func (r CreateCardCheckoutRequest) validate() error {
	if err := validateRequired("CustomerCode", r.CustomerCode); err != nil {
		return err
	}
	if utf8.RuneCountInString(r.CustomerCode) != 9 {
		return &ValidationError{Field: "CustomerCode", Message: "must contain 9 characters"}
	}
	if r.MerchantID != "" {
		if len(r.MerchantID) != 15 {
			return &ValidationError{Field: "MerchantID", Message: "acquiring merchant ID must contain 15 digits"}
		}
		for _, c := range r.MerchantID {
			if c < '0' || c > '9' {
				return &ValidationError{Field: "MerchantID", Message: "acquiring merchant ID must contain 15 digits"}
			}
		}
	}
	if err := validatePositiveAmount("Amount", r.Amount); err != nil {
		return err
	}
	if err := validateRequired("Purpose", r.Purpose); err != nil {
		return err
	}
	if utf8.RuneCountInString(r.Purpose) > 140 {
		return &ValidationError{Field: "Purpose", Message: "exceeds 140 characters"}
	}
	for field, value := range map[string]string{"RedirectURL": r.RedirectURL, "FailRedirectURL": r.FailRedirectURL} {
		if err := validateOptionalURL(field, value); err != nil {
			return err
		}
	}
	if r.TTLMinutes < 0 || r.TTLMinutes > 44640 {
		return &ValidationError{Field: "TTLMinutes", Message: "must be zero (bank default) or between 1 and 44640"}
	}
	if utf8.RuneCountInString(r.PaymentLinkID) > 45 {
		return &ValidationError{Field: "PaymentLinkID", Message: "exceeds 45 characters"}
	}
	return nil
}

// GetPayment retrieves current status and approval/refund records for a checkout.
// It preserves native status strings, including future values.
func (s *AcquiringService) GetPayment(ctx context.Context, operationID string) (*AcquiringPayment, error) {
	if err := validatePathID("OperationID", operationID); err != nil {
		return nil, err
	}
	var out dataEnvelope[acquiringPaymentWire]
	if err := s.client.do(ctx, http.MethodGet, "/acquiring/v1.0/payments/"+operationID, nil, nil, &out); err != nil {
		return nil, err
	}
	p := out.Data
	if p.OperationID == "" || p.Status == "" || p.Amount <= 0 {
		return nil, &RequestError{Method: http.MethodGet, Path: "/acquiring/v1.0/payments/" + operationID, Err: fmt.Errorf("payment response lacks operationId, status, or a positive amount")}
	}
	payment := &AcquiringPayment{
		Checkout: p.checkout(), CustomerCode: p.CustomerCode, PaymentType: p.PaymentType,
		PaymentID: p.PaymentID, TransactionID: p.TransactionID, ConsumerID: p.ConsumerID,
		CreatedAt: p.CreatedAt, PaidAt: p.PaidAt,
	}
	for _, order := range p.Orders {
		payment.Orders = append(payment.Orders, AcquiringOrder{
			OrderID: order.OrderID, Type: order.Type, Amount: Money(order.Amount), Time: order.Time,
		})
	}
	return payment, nil
}

// Refund requests a full or partial refund of a payment accepted through checkout.
// The documented eligible payment status is APPROVED. This method does not issue
// an implicit GetPayment request or retry. Persist and reconcile its result in
// the application, especially when an error reports an unknown outcome.
func (s *AcquiringService) Refund(ctx context.Context, operationID string, req RefundAcquiringRequest) (*AcquiringRefund, error) {
	if err := validatePathID("OperationID", operationID); err != nil {
		return nil, err
	}
	if err := validatePositiveAmount("Amount", req.Amount); err != nil {
		return nil, err
	}
	body := dataEnvelope[struct {
		Amount rubleMoney `json:"amount"`
	}]{}
	body.Data.Amount = rubleMoney(req.Amount)
	var out dataEnvelope[json.RawMessage]
	if err := s.client.do(ctx, http.MethodPost, "/acquiring/v1.0/payments/"+operationID+"/refund", nil, body, &out); err != nil {
		return nil, err
	}
	var result struct {
		IsRefund    *bool           `json:"isRefund"`
		Amount      rubleMoney      `json:"amount"`
		OperationID string          `json:"operationId"`
		Status      AcquiringStatus `json:"status"`
	}
	refund := &AcquiringRefund{RawData: append(json.RawMessage(nil), out.Data...)}
	if err := json.Unmarshal(out.Data, &result); err != nil {
		return refund, &RequestError{Method: http.MethodPost, Path: "/acquiring/v1.0/payments/" + operationID + "/refund", Err: fmt.Errorf("decode refund response: %w", err), OutcomeUnknown: true}
	}
	refund.IsRefund = result.IsRefund
	refund.Amount = Money(result.Amount)
	refund.OperationID = result.OperationID
	refund.Status = result.Status
	if result.IsRefund == nil && result.OperationID == "" && result.Status == "" {
		return refund, &RequestError{Method: http.MethodPost, Path: "/acquiring/v1.0/payments/" + operationID + "/refund", Err: fmt.Errorf("refund response has no recognizable result; retrieve payment status before retrying"), OutcomeUnknown: true}
	}
	return refund, nil
}
