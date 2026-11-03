package paytochkabank

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// SBPService handles direct dynamic SBP QR payments and their refunds.
// Its merchant identifiers are different from internet-acquiring merchant IDs.
type SBPService struct {
	client *Client
}

// QRImageParams requests a QR image from the bank. Omit it to use bank defaults.
// Dimensions must be between 200 and 2000 pixels when supplied.
type QRImageParams struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MediaType string `json:"mediaType,omitempty"`
}

// QRImage preserves the bank's image format. PNG Content is base64 encoded;
// SVG Content is XML. Treat SVG as untrusted markup when displaying it.
type QRImage struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MediaType string `json:"mediaType"`
	Content   string `json:"content"`
}

// Bytes decodes the returned image without changing or rendering it.
func (image QRImage) Bytes() ([]byte, error) {
	switch image.MediaType {
	case "image/png":
		content, err := base64.StdEncoding.DecodeString(image.Content)
		if err != nil {
			return nil, fmt.Errorf("paytochkabank: decode QR PNG: %w", err)
		}
		return content, nil
	case "image/svg+xml":
		return []byte(image.Content), nil
	default:
		return nil, fmt.Errorf("paytochkabank: unsupported QR image media type %q", image.MediaType)
	}
}

// CreateDynamicQRRequest creates a one-use QR code for the given amount.
// AccountID is the account number and bank BIC separated by '/'.
// TTLMinutes is optional; zero uses the bank's default (72 hours).
type CreateDynamicQRRequest struct {
	MerchantID     string
	AccountID      string
	Amount         Money
	PaymentPurpose string
	TTLMinutes     int
	RedirectURL    string
	SourceName     string
	ImageParams    *QRImageParams
}

// DynamicQR contains the IDs and display data returned after registration.
// Payload is the bank-app payment URL; QRCID must be retained for refunds.
type DynamicQR struct {
	QRCID   string   `json:"qrcId"`
	Payload string   `json:"payload"`
	Image   *QRImage `json:"image,omitempty"`
}

// CreateDynamicQR registers a one-use, fixed-amount QR code (qrcType "02").
// The bank expects the amount in kopecks, unlike the direct refund endpoint.
func (service *SBPService) CreateDynamicQR(ctx context.Context, request CreateDynamicQRRequest) (*DynamicQR, error) {
	if err := validatePathID("merchantID", request.MerchantID); err != nil {
		return nil, err
	}
	accountParts := strings.Split(request.AccountID, "/")
	if len(accountParts) != 2 {
		return nil, &ValidationError{Field: "accountID", Message: "must contain account number and bank BIC separated by '/'"}
	}
	for i, value := range accountParts {
		if err := validatePathID([]string{"account number", "bank BIC"}[i], value); err != nil {
			return nil, err
		}
	}
	if err := validatePositiveAmount("amount", request.Amount); err != nil {
		return nil, err
	}
	if err := validateRequired("paymentPurpose", request.PaymentPurpose); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(request.PaymentPurpose) > 140 {
		return nil, &ValidationError{Field: "paymentPurpose", Message: "exceeds 140 characters"}
	}
	if request.TTLMinutes < 0 || request.TTLMinutes > 129600 {
		return nil, &ValidationError{Field: "ttlMinutes", Message: "must be zero or between 1 and 129600"}
	}
	if err := validateOptionalURL("redirectURL", request.RedirectURL); err != nil {
		return nil, err
	}
	if request.RedirectURL != "" {
		parsed, _ := url.Parse(request.RedirectURL)
		if parsed.Scheme != "https" {
			return nil, &ValidationError{Field: "redirectURL", Message: "must use HTTPS"}
		}
	}
	if image := request.ImageParams; image != nil {
		if image.Width < 200 || image.Width > 2000 || image.Height < 200 || image.Height > 2000 {
			return nil, &ValidationError{Field: "imageParams", Message: "dimensions must be between 200 and 2000 pixels"}
		}
		if image.MediaType != "" && image.MediaType != "image/png" && image.MediaType != "image/svg+xml" {
			return nil, &ValidationError{Field: "imageParams.mediaType", Message: "must be image/png or image/svg+xml"}
		}
	}
	type createQRBody struct {
		Amount         Money          `json:"amount"`
		Currency       string         `json:"currency"`
		PaymentPurpose string         `json:"paymentPurpose"`
		QRCType        string         `json:"qrcType"`
		TTL            int            `json:"ttl,omitempty"`
		RedirectURL    string         `json:"redirectUrl,omitempty"`
		SourceName     string         `json:"sourceName,omitempty"`
		ImageParams    *QRImageParams `json:"imageParams,omitempty"`
	}
	body := dataEnvelope[createQRBody]{Data: createQRBody{request.Amount, "RUB", request.PaymentPurpose, "02", request.TTLMinutes, request.RedirectURL, request.SourceName, request.ImageParams}}
	path := "/sbp/v1.0/qr-code/merchant/" + url.PathEscape(request.MerchantID) + "/" + url.PathEscape(accountParts[0]) + "/" + url.PathEscape(accountParts[1])
	var response dataEnvelope[DynamicQR]
	if err := service.client.do(ctx, http.MethodPost, path, nil, body, &response); err != nil {
		return nil, err
	}
	if response.Data.QRCID == "" || response.Data.Payload == "" {
		return nil, &RequestError{Method: http.MethodPost, Path: path, Err: errors.New("QR response is missing qrcId or payload"), OutcomeUnknown: true}
	}
	return &response.Data, nil
}

// SBPPaymentStatus is the bank's native payment state. Unknown values remain
// available as strings so an API extension cannot silently become "paid".
type SBPPaymentStatus string

const (
	SBPPaymentConfirming SBPPaymentStatus = "Confirming"
	SBPPaymentConfirmed  SBPPaymentStatus = "Confirmed"
	SBPPaymentInitiated  SBPPaymentStatus = "Initiated"
	SBPPaymentAccepting  SBPPaymentStatus = "Accepting"
	SBPPaymentAccepted   SBPPaymentStatus = "Accepted"
	SBPPaymentInProgress SBPPaymentStatus = "InProgress"
	SBPPaymentRejected   SBPPaymentStatus = "Rejected"
	SBPPaymentError      SBPPaymentStatus = "Error"
	SBPPaymentTimeout    SBPPaymentStatus = "Timeout"
)

// SBPPayment is a payment returned by the documented Get Payments endpoint.
type SBPPayment struct {
	QRCID            string           `json:"qrcId"`
	Status           SBPPaymentStatus `json:"status"`
	Message          string           `json:"message"`
	RefTransactionID string           `json:"refTransactionId"`
}

// GetSBPPaymentsRequest filters historical payments. Set FromDate explicitly
// for older payments; without it the bank searches only yesterday and today.
// Dates use YYYY-MM-DD. Zero pagination values use the bank's defaults.
type GetSBPPaymentsRequest struct {
	CustomerCode string
	QRCID        string
	FromDate     string
	ToDate       string
	Page         int
	PerPage      int
}

// SBPPaymentsPage retains the bank's pagination metadata.
type SBPPaymentsPage struct {
	Payments   []SBPPayment
	TotalPages int
}

// GetPayments retrieves payment states and the identifiers needed for refunds.
func (service *SBPService) GetPayments(ctx context.Context, request GetSBPPaymentsRequest) (*SBPPaymentsPage, error) {
	if err := validateRequired("customerCode", request.CustomerCode); err != nil {
		return nil, err
	}
	if request.Page < 0 || request.PerPage < 0 {
		return nil, &ValidationError{Field: "pagination", Message: "page and perPage cannot be negative"}
	}
	for name, value := range map[string]string{"fromDate": request.FromDate, "toDate": request.ToDate} {
		if value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return nil, &ValidationError{Field: name, Message: "must be a date in YYYY-MM-DD format"}
			}
		}
	}
	if request.FromDate != "" && request.ToDate != "" && request.FromDate > request.ToDate {
		return nil, &ValidationError{Field: "toDate", Message: "must not precede fromDate"}
	}
	query := url.Values{"customerCode": {request.CustomerCode}}
	for key, value := range map[string]string{"qrcId": request.QRCID, "fromDate": request.FromDate, "toDate": request.ToDate} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if request.Page > 0 {
		query.Set("page", strconv.Itoa(request.Page))
	}
	if request.PerPage > 0 {
		query.Set("perPage", strconv.Itoa(request.PerPage))
	}
	var response struct {
		Data struct {
			Payments []SBPPayment `json:"Payments"`
		} `json:"Data"`
		Meta struct {
			TotalPages int `json:"totalPages"`
		} `json:"Meta"`
	}
	if err := service.client.do(ctx, http.MethodGet, "/sbp/v1.0/get-sbp-payments", query, nil, &response); err != nil {
		return nil, err
	}
	return &SBPPaymentsPage{Payments: response.Data.Payments, TotalPages: response.Meta.TotalPages}, nil
}

// QRPaymentStatus retains the Data payload from Get Qr Codes Payment Status.
// The currently accessible bank reference omits its response schema. Rather
// than guessing a shape, this draft preserves the complete JSON. Use GetPayments
// for documented typed payment states, or verified incomingSbpPayment events.
type QRPaymentStatus struct {
	Data json.RawMessage
}

// GetQRPaymentStatus retrieves the raw bank status payload for one dynamic QR.
// Status lookups are available during QR validity or for 24 hours after payment.
func (service *SBPService) GetQRPaymentStatus(ctx context.Context, qrcID string) (*QRPaymentStatus, error) {
	if err := validatePathID("qrcID", qrcID); err != nil {
		return nil, err
	}
	var response dataEnvelope[json.RawMessage]
	path := "/sbp/v1.0/qr-codes/" + url.PathEscape(qrcID) + "/payment-status"
	if err := service.client.do(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, err
	}
	return &QRPaymentStatus{Data: response.Data}, nil
}

// RefundSBPRequest refunds an already accepted dynamic QR payment. Amount is
// explicit for both full and partial refunds. Supply exactly one transaction ID.
// A known digital-ruble TrxID is rejected locally: its refunds require the bank UI.
// When using RefTransactionID, the application must establish the payment rail.
type RefundSBPRequest struct {
	BankCode         string
	AccountCode      string
	Amount           Money
	QRCID            string
	Purpose          string
	RefTransactionID string
	TrxID            string
}

// SBPRefundStatus is the bank's native refund process state.
type SBPRefundStatus string

const (
	SBPRefundWaitingForClientConfirm SBPRefundStatus = "WaitingForClientConfirm"
	SBPRefundInitiated               SBPRefundStatus = "Initiated"
	SBPRefundWaitingForConfirm       SBPRefundStatus = "WaitingForConfirm"
	SBPRefundConfirmed               SBPRefundStatus = "Confirmed"
	SBPRefundWaitingForAccept        SBPRefundStatus = "WaitingForAccept"
	SBPRefundAccepted                SBPRefundStatus = "Accepted"
	SBPRefundRejected                SBPRefundStatus = "Rejected"
)

// SBPRefund is a refund request, which can still be pending after HTTP success.
type SBPRefund struct {
	RequestID         string          `json:"requestId"`
	Status            SBPRefundStatus `json:"status"`
	StatusDescription string          `json:"statusDescription,omitempty"`
}

// Completed reports final successful completion, not merely request acceptance.
func (refund SBPRefund) Completed() bool {
	return refund.Status == SBPRefundAccepted
}

// Final reports the two documented final states. Unknown states are not final.
func (refund SBPRefund) Final() bool {
	return refund.Status == SBPRefundAccepted || refund.Status == SBPRefundRejected
}

// Refund starts a full or partial refund. It performs one HTTP request and does
// not poll, retry, check available balances, or apply application-level policy.
func (service *SBPService) Refund(ctx context.Context, request RefundSBPRequest) (*SBPRefund, error) {
	for _, item := range []struct{ name, value string }{
		{"bankCode", request.BankCode}, {"accountCode", request.AccountCode}, {"qrcID", request.QRCID},
	} {
		if err := validateRequired(item.name, item.value); err != nil {
			return nil, err
		}
	}
	if len(request.BankCode) > 9 || len(request.AccountCode) > 20 {
		return nil, &ValidationError{Field: "account", Message: "bankCode cannot exceed 9 or accountCode 20 characters"}
	}
	if err := validatePositiveAmount("amount", request.Amount); err != nil {
		return nil, err
	}
	if (request.RefTransactionID == "") == (request.TrxID == "") {
		return nil, &ValidationError{Field: "transactionID", Message: "supply exactly one of refTransactionID and trxID"}
	}
	if request.RefTransactionID != "" {
		if err := validateRequired("refTransactionID", request.RefTransactionID); err != nil {
			return nil, err
		}
	}
	if request.TrxID != "" {
		if err := validateRequired("trxID", request.TrxID); err != nil {
			return nil, err
		}
		if len(request.TrxID) == 36 {
			return nil, fmt.Errorf("%w: digital-ruble refunds require internet banking", ErrUnsupportedPaymentMethod)
		}
	}
	if utf8.RuneCountInString(request.Purpose) > 140 {
		return nil, &ValidationError{Field: "purpose", Message: "exceeds 140 characters"}
	}
	type refundBody struct {
		BankCode         string `json:"bankCode"`
		AccountCode      string `json:"accountCode"`
		Amount           string `json:"amount"`
		Currency         string `json:"currency"`
		QRCID            string `json:"qrcId"`
		Purpose          string `json:"purpose,omitempty"`
		RefTransactionID string `json:"refTransactionId,omitempty"`
		TrxID            string `json:"trxId,omitempty"`
	}
	body := dataEnvelope[refundBody]{Data: refundBody{
		BankCode: request.BankCode, AccountCode: request.AccountCode, Amount: request.Amount.Rubles(),
		Currency: "RUB", QRCID: request.QRCID, Purpose: request.Purpose,
		RefTransactionID: request.RefTransactionID, TrxID: request.TrxID,
	}}
	var response dataEnvelope[SBPRefund]
	if err := service.client.do(ctx, http.MethodPost, "/sbp/v1.0/refund", nil, body, &response); err != nil {
		return nil, err
	}
	if response.Data.RequestID == "" || response.Data.Status == "" {
		return nil, &RequestError{Method: http.MethodPost, Path: "/sbp/v1.0/refund", Err: errors.New("refund response is missing requestId or status"), OutcomeUnknown: true}
	}
	return &response.Data, nil
}

// GetRefund retrieves a refund's state; Accepted is the final successful state.
func (service *SBPService) GetRefund(ctx context.Context, requestID string) (*SBPRefund, error) {
	if err := validatePathID("requestID", requestID); err != nil {
		return nil, err
	}
	var response dataEnvelope[SBPRefund]
	if err := service.client.do(ctx, http.MethodGet, "/sbp/v1.0/refund/"+url.PathEscape(requestID), nil, nil, &response); err != nil {
		return nil, err
	}
	if response.Data.RequestID == "" || response.Data.Status == "" {
		return nil, &RequestError{Method: http.MethodGet, Path: "/sbp/v1.0/refund/" + url.PathEscape(requestID), Err: errors.New("refund response is missing requestId or status")}
	}
	return &response.Data, nil
}
