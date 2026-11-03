package paytochkabank

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAcquiringTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient("test.jwt.token", WithBaseURL(server.URL+"/uapi"), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func validCardCheckoutRequest() CreateCardCheckoutRequest {
	return CreateCardCheckoutRequest{
		CustomerCode: "1234567ab", MerchantID: "200000000001097", Amount: 123456,
		Purpose: "Order 42", RedirectURL: "https://shop.example/success",
		FailRedirectURL: "https://shop.example/failure", TTLMinutes: 30, PaymentLinkID: "order-42-attempt-1",
	}
}

func TestCreateCardCheckoutWireContract(t *testing.T) {
	client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/uapi/acquiring/v1.0/payments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test.jwt.token" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Data map[string]json.RawMessage `json:"Data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if got := string(body.Data["amount"]); got != "1234.56" {
			t.Errorf("wire amount = %s, want ruble number 1234.56", got)
		}
		if got := string(body.Data["paymentMode"]); got != `["card"]` {
			t.Errorf("paymentMode = %s", got)
		}
		if _, ok := body.Data["preAuthorization"]; ok {
			t.Error("ordinary checkout unexpectedly specifies a hold")
		}
		if _, ok := body.Data["Items"]; ok {
			t.Error("checkout unexpectedly supplies fiscalization items")
		}
		if got := string(body.Data["paymentLinkId"]); got != `"order-42-attempt-1"` {
			t.Errorf("paymentLinkId = %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Data":{"operationId":"checkout-42","paymentLink":"https://pay.example/42","amount":"1234.56","status":"CREATED","purpose":"Order 42","merchantId":"200000000001097","ttl":30,"paymentLinkId":"order-42-attempt-1","paymentMode":["card"]}}`)
	})
	checkout, err := client.Acquiring.CreateCardCheckout(context.Background(), validCardCheckoutRequest())
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Amount != 123456 || checkout.OperationID != "checkout-42" || checkout.Status != AcquiringCreated || checkout.PaymentLink != "https://pay.example/42" {
		t.Fatalf("unexpected checkout: %+v", checkout)
	}
}

func TestCardCheckoutValidationMakesNoRequest(t *testing.T) {
	client := newAcquiringTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid input reached bank")
	})
	tests := []struct {
		name string
		edit func(*CreateCardCheckoutRequest)
	}{
		{"zero amount", func(r *CreateCardCheckoutRequest) { r.Amount = 0 }},
		{"negative amount", func(r *CreateCardCheckoutRequest) { r.Amount = -1 }},
		{"missing customer", func(r *CreateCardCheckoutRequest) { r.CustomerCode = "" }},
		{"short customer", func(r *CreateCardCheckoutRequest) { r.CustomerCode = "123" }},
		{"wrong merchant service", func(r *CreateCardCheckoutRequest) { r.MerchantID = "MA0000000001" }},
		{"non-digit merchant", func(r *CreateCardCheckoutRequest) { r.MerchantID = "20000000000109x" }},
		{"blank purpose", func(r *CreateCardCheckoutRequest) { r.Purpose = "   " }},
		{"long purpose", func(r *CreateCardCheckoutRequest) { r.Purpose = strings.Repeat("я", 141) }},
		{"relative redirect", func(r *CreateCardCheckoutRequest) { r.RedirectURL = "/success" }},
		{"negative ttl", func(r *CreateCardCheckoutRequest) { r.TTLMinutes = -1 }},
		{"ttl too long", func(r *CreateCardCheckoutRequest) { r.TTLMinutes = 44641 }},
		{"long order identifier", func(r *CreateCardCheckoutRequest) { r.PaymentLinkID = strings.Repeat("x", 46) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validCardCheckoutRequest()
			tt.edit(&req)
			_, err := client.Acquiring.CreateCardCheckout(context.Background(), req)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
		})
	}
}

func TestGetAcquiringPaymentKeepsRefundRecordsAndNativeStatus(t *testing.T) {
	client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/uapi/acquiring/v1.0/payments/checkout-42" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"Data":{"operationId":"checkout-42","paymentLink":"https://pay.example/42","amount":1234.56,"status":"FUTURE_STATUS","paymentType":"card","paymentId":"bank-payment-id","createdAt":"2026-11-02T10:00:00+03:00","Order":[{"orderId":"approval-1","type":"approval","amount":"1234.56","time":"2026-11-02T10:01:00+03:00"},{"orderId":"refund-1","type":"refund","amount":12.34,"time":"2026-11-02T10:02:00+03:00"}]}}`)
	})
	payment, err := client.Acquiring.GetPayment(context.Background(), "checkout-42")
	if err != nil {
		t.Fatal(err)
	}
	if payment.Status != "FUTURE_STATUS" || payment.PaymentID != "bank-payment-id" || len(payment.Orders) != 2 {
		t.Fatalf("unexpected payment: %+v", payment)
	}
	if payment.Orders[0].Amount != 123456 || payment.Orders[1].Amount != 1234 || payment.Orders[1].OrderID != "refund-1" {
		t.Fatalf("unexpected refund records: %+v", payment.Orders)
	}
}

func TestAcquiringRefundFullAndPartialUseExplicitAmount(t *testing.T) {
	for _, amount := range []Money{123456, 1234} {
		t.Run(amount.Rubles(), func(t *testing.T) {
			calls := 0
			client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/uapi/acquiring/v1.0/payments/checkout-42/refund" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				var body struct {
					Data struct {
						Amount json.Number `json:"amount"`
					} `json:"Data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if string(body.Data.Amount) != amount.Rubles() {
					t.Errorf("refund wire amount = %s, want %s", body.Data.Amount, amount.Rubles())
				}
				_, _ = io.WriteString(w, `{"Data":{"isRefund":true,"operationId":"refund-42","amount":`+amount.Rubles()+`,"newBankField":"preserved"}}`)
			})
			refund, err := client.Acquiring.Refund(context.Background(), "checkout-42", RefundAcquiringRequest{Amount: amount})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || refund.IsRefund == nil || !*refund.IsRefund || refund.Amount != amount || refund.OperationID != "refund-42" || !strings.Contains(string(refund.RawData), "newBankField") {
				t.Fatalf("calls=%d, unexpected refund: %+v", calls, refund)
			}
		})
	}
}

func TestAcquiringRefundMissingOptionalBooleanIsNotRejection(t *testing.T) {
	client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Data":{"operationId":"refund-42","status":"ON-REFUND"}}`)
	})
	refund, err := client.Acquiring.Refund(context.Background(), "checkout-42", RefundAcquiringRequest{Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if refund.IsRefund != nil || refund.Status != AcquiringOnRefund {
		t.Fatalf("unexpected refund: %+v", refund)
	}
}

func TestAcquiringInvalidIDsAndRefundAmountsMakeNoRequest(t *testing.T) {
	client := newAcquiringTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid input reached bank")
	})
	for _, id := range []string{"", "../payments", "operation/id", "operation?query", "operation#fragment"} {
		if _, err := client.Acquiring.GetPayment(context.Background(), id); err == nil {
			t.Errorf("GetPayment accepted invalid ID %q", id)
		}
		if _, err := client.Acquiring.Refund(context.Background(), id, RefundAcquiringRequest{Amount: 1}); err == nil {
			t.Errorf("Refund accepted invalid ID %q", id)
		}
	}
	for _, amount := range []Money{0, -1} {
		if _, err := client.Acquiring.Refund(context.Background(), "valid-id", RefundAcquiringRequest{Amount: amount}); err == nil {
			t.Errorf("Refund accepted amount %d", amount)
		}
	}
}

func TestAcquiringInvalidSuccessfulResponsesHaveUnknownWriteOutcome(t *testing.T) {
	for _, body := range []string{`{"Data":{}}`, `{"Data":{"amount":"0.001"}}`, `{"Data":null}`, `{"Data":[]}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = io.WriteString(w, body)
			})
			if _, err := client.Acquiring.CreateCardCheckout(context.Background(), validCardCheckoutRequest()); !IsOutcomeUnknown(err) {
				t.Errorf("CreateCardCheckout error = %v, want unknown outcome", err)
			}
			if _, err := client.Acquiring.Refund(context.Background(), "checkout-42", RefundAcquiringRequest{Amount: 100}); !IsOutcomeUnknown(err) {
				t.Errorf("Refund error = %v, want unknown outcome", err)
			}
			if calls != 2 {
				t.Errorf("calls = %d, expected exactly one request per operation", calls)
			}
		})
	}
}

func TestCardCheckoutResponseStatusIsOptional(t *testing.T) {
	client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Data":{"operationId":"checkout-42","paymentLink":"https://pay.example/42","amount":1234.56}}`)
	})
	checkout, err := client.Acquiring.CreateCardCheckout(context.Background(), validCardCheckoutRequest())
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Status != "" || checkout.OperationID != "checkout-42" {
		t.Fatalf("optional status was invented or result lost: %+v", checkout)
	}
}

func TestAcquiringRefundPreservesDiagnosticRawDataOnResponseError(t *testing.T) {
	for _, raw := range []string{`{"futureRefundResult":"pending"}`, `{"amount":"invalid"}`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			client := newAcquiringTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"Data":`+raw+`}`)
			})
			refund, err := client.Acquiring.Refund(context.Background(), "checkout-42", RefundAcquiringRequest{Amount: 100})
			if !IsOutcomeUnknown(err) {
				t.Fatalf("error = %v, want unknown outcome", err)
			}
			if refund == nil || string(refund.RawData) != raw {
				t.Fatalf("response diagnostics lost: %+v", refund)
			}
		})
	}
}
