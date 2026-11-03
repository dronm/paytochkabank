package paytochkabank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func newSBPTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient("test-token", WithBaseURL(server.URL+"/uapi"))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCreateDynamicQRWireContract(t *testing.T) {
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/uapi/sbp/v1.0/qr-code/merchant/MF0000000001/40817810802000000008/044525104" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Data map[string]json.RawMessage `json:"Data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for name, want := range map[string]string{"amount": "12345", "qrcType": `"02"`, "currency": `"RUB"`, "ttl": "15"} {
			if got := string(body.Data[name]); got != want {
				t.Errorf("%s = %s, want %s", name, got, want)
			}
		}
		fmt.Fprint(w, `{"Data":{"qrcId":"AS000000000000000000000000000001","payload":"https://qr.nspk.ru/AS000000000000000000000000000001","image":{"width":300,"height":300,"mediaType":"image/png","content":"cG5n"}}}`)
	})
	qr, err := client.SBP.CreateDynamicQR(context.Background(), CreateDynamicQRRequest{
		MerchantID: "MF0000000001", AccountID: "40817810802000000008/044525104", Amount: 12345,
		PaymentPurpose: "Order 42", TTLMinutes: 15, RedirectURL: "https://shop.example/payment", ImageParams: &QRImageParams{Width: 300, Height: 300, MediaType: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if qr.QRCID != "AS000000000000000000000000000001" || qr.Image == nil {
		t.Fatalf("unexpected QR: %+v", qr)
	}
	image, err := qr.Image.Bytes()
	if err != nil || string(image) != "png" {
		t.Fatalf("image = %q, %v", image, err)
	}
}

func TestCreateDynamicQROmitsBankDefaults(t *testing.T) {
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body dataEnvelope[map[string]json.RawMessage]
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, field := range []string{"ttl", "imageParams", "redirectUrl", "sourceName"} {
			if _, ok := body.Data[field]; ok {
				t.Errorf("default field %s must be omitted", field)
			}
		}
		fmt.Fprint(w, `{"Data":{"qrcId":"AS1","payload":"https://qr.nspk.ru/AS1"}}`)
	})
	_, err := client.SBP.CreateDynamicQR(context.Background(), validQRRequest())
	if err != nil {
		t.Fatal(err)
	}
}

func validQRRequest() CreateDynamicQRRequest {
	return CreateDynamicQRRequest{MerchantID: "MF0000000001", AccountID: "40817810802000000008/044525104", Amount: 12345, PaymentPurpose: "Order 42"}
}

func TestCreateDynamicQRValidationMakesNoRequests(t *testing.T) {
	calls := 0
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	for name, change := range map[string]func(*CreateDynamicQRRequest){
		"missing merchant":      func(r *CreateDynamicQRRequest) { r.MerchantID = "" },
		"path injection":        func(r *CreateDynamicQRRequest) { r.MerchantID = "../merchant" },
		"encoded slash":         func(r *CreateDynamicQRRequest) { r.MerchantID = "merchant%2Fother" },
		"missing account BIC":   func(r *CreateDynamicQRRequest) { r.AccountID = "40817810802000000008" },
		"empty account BIC":     func(r *CreateDynamicQRRequest) { r.AccountID = "40817810802000000008/" },
		"extra account segment": func(r *CreateDynamicQRRequest) { r.AccountID += "/other" },
		"zero amount":           func(r *CreateDynamicQRRequest) { r.Amount = 0 },
		"negative amount":       func(r *CreateDynamicQRRequest) { r.Amount = -1 },
		"empty purpose":         func(r *CreateDynamicQRRequest) { r.PaymentPurpose = " " },
		"long purpose":          func(r *CreateDynamicQRRequest) { r.PaymentPurpose = strings.Repeat("Я", 141) },
		"negative ttl":          func(r *CreateDynamicQRRequest) { r.TTLMinutes = -1 },
		"excessive ttl":         func(r *CreateDynamicQRRequest) { r.TTLMinutes = 129601 },
		"insecure redirect":     func(r *CreateDynamicQRRequest) { r.RedirectURL = "http://shop.example/" },
		"invalid image size":    func(r *CreateDynamicQRRequest) { r.ImageParams = &QRImageParams{Width: 199, Height: 300} },
		"invalid image format": func(r *CreateDynamicQRRequest) {
			r.ImageParams = &QRImageParams{Width: 300, Height: 300, MediaType: "image/jpeg"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := validQRRequest()
			change(&request)
			_, err := client.SBP.CreateDynamicQR(context.Background(), request)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid inputs made %d requests", calls)
	}
}

func TestSBPRefundWireUsesRubleStringAndOneTransactionID(t *testing.T) {
	for _, useReference := range []bool{false, true} {
		t.Run(fmt.Sprintf("reference_%t", useReference), func(t *testing.T) {
			calls := 0
			client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/uapi/sbp/v1.0/refund" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body dataEnvelope[map[string]json.RawMessage]
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if got := string(body.Data["amount"]); got != `"123.45"` {
					t.Errorf("refund amount = %s; want decimal RUB string", got)
				}
				if _, reference := body.Data["refTransactionId"]; reference != useReference {
					t.Error("incorrect refTransactionId presence")
				}
				if _, trx := body.Data["trxId"]; trx == useReference {
					t.Error("incorrect trxId presence")
				}
				fmt.Fprint(w, `{"Data":{"requestId":"openapi-refund-1","status":"Confirmed"}}`)
			})
			request := validSBPRefund()
			if useReference {
				request.RefTransactionID, request.TrxID = "ref-transaction-1", ""
			}
			refund, err := client.SBP.Refund(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || refund.RequestID != "openapi-refund-1" || refund.Completed() || refund.Final() {
				t.Fatalf("request accepted as completed or missing identifier: %+v, calls=%d", refund, calls)
			}
		})
	}
}

func validSBPRefund() RefundSBPRequest {
	return RefundSBPRequest{BankCode: "044525104", AccountCode: "40817810802000000008", Amount: 12345, QRCID: "AS1", TrxID: "A1A2S3D5F6G7H8J9K0C4S5C6D7V5D1K2"}
}

func TestSBPRefundValidation(t *testing.T) {
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid refund reached bank") })
	for name, change := range map[string]func(*RefundSBPRequest){
		"empty transaction IDs":  func(r *RefundSBPRequest) { r.TrxID = "" },
		"both transaction IDs":   func(r *RefundSBPRequest) { r.RefTransactionID = "ref1" },
		"whitespace transaction": func(r *RefundSBPRequest) { r.TrxID = " " },
		"zero refund":            func(r *RefundSBPRequest) { r.Amount = 0 },
		"missing bank":           func(r *RefundSBPRequest) { r.BankCode = "" },
		"long account":           func(r *RefundSBPRequest) { r.AccountCode = strings.Repeat("1", 21) },
		"missing QR":             func(r *RefundSBPRequest) { r.QRCID = " " },
		"long purpose":           func(r *RefundSBPRequest) { r.Purpose = strings.Repeat("Я", 141) },
	} {
		t.Run(name, func(t *testing.T) {
			request := validSBPRefund()
			change(&request)
			if _, err := client.SBP.Refund(context.Background(), request); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
	request := validSBPRefund()
	request.TrxID = "48232c9a-ce82-1593-3cb6-5c85a1ffef8f"
	if _, err := client.SBP.Refund(context.Background(), request); !errors.Is(err, ErrUnsupportedPaymentMethod) {
		t.Fatalf("digital-ruble error = %v", err)
	}
}

func TestSBPRefundFinalStatesAndUnknownState(t *testing.T) {
	for _, status := range []SBPRefundStatus{SBPRefundAccepted, SBPRefundRejected, SBPRefundConfirmed, "NEW_BANK_STATE"} {
		client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/uapi/sbp/v1.0/refund/openapi-1" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			fmt.Fprintf(w, `{"Data":{"requestId":"openapi-1","status":%q,"statusDescription":"details"}}`, status)
		})
		refund, err := client.SBP.GetRefund(context.Background(), "openapi-1")
		if err != nil || refund.Status != status {
			t.Fatalf("refund = %+v, %v", refund, err)
		}
		if refund.Completed() != (status == SBPRefundAccepted) || refund.Final() != (status == SBPRefundAccepted || status == SBPRefundRejected) {
			t.Errorf("incorrect final/completed classification for %s", status)
		}
	}
}

func TestGetSBPPaymentsPreservesIdentifiersAndPagination(t *testing.T) {
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := map[string][]string{"customerCode": {"1234567ab"}, "qrcId": {"AS1"}, "fromDate": {"2026-10-01"}, "toDate": {"2026-10-31"}, "page": {"2"}, "perPage": {"10"}}
		if r.Method != http.MethodGet || r.URL.Path != "/uapi/sbp/v1.0/get-sbp-payments" || !reflect.DeepEqual(map[string][]string(r.URL.Query()), want) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `{"Data":{"Payments":[{"qrcId":"AS1","status":"Accepted","message":"success","refTransactionId":"ref1"}]},"Meta":{"totalPages":3}}`)
	})
	page, err := client.SBP.GetPayments(context.Background(), GetSBPPaymentsRequest{CustomerCode: "1234567ab", QRCID: "AS1", FromDate: "2026-10-01", ToDate: "2026-10-31", Page: 2, PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalPages != 3 || len(page.Payments) != 1 || page.Payments[0].RefTransactionID != "ref1" || page.Payments[0].Status != SBPPaymentAccepted {
		t.Fatalf("unexpected payments: %+v", page)
	}
}

func TestGetQRPaymentStatusPreservesRawData(t *testing.T) {
	// Deliberately tests pass-through, not an assumed bank schema. The current
	// accessible reference omits this endpoint's response model.
	const raw = `{"futureSchema":[{"trxId":"original-id","extra":true}]}`
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/uapi/sbp/v1.0/qr-codes/AS1/payment-status" {
			t.Errorf("unexpected status URL %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"Data":%s}`, raw)
	})
	status, err := client.SBP.GetQRPaymentStatus(context.Background(), "AS1")
	if err != nil || string(status.Data) != raw {
		t.Fatalf("raw status = %+v, %v", status, err)
	}
}

func TestSBPMalformedWriteResponsesAreUncertain(t *testing.T) {
	client := newSBPTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"Data":{}}`) })
	_, qrErr := client.SBP.CreateDynamicQR(context.Background(), validQRRequest())
	_, refundErr := client.SBP.Refund(context.Background(), validSBPRefund())
	for _, err := range []error{qrErr, refundErr} {
		var request *RequestError
		if !errors.As(err, &request) || !request.OutcomeUnknown {
			t.Errorf("missing identifiers should be uncertain: %v", err)
		}
	}
}

func TestQRImageBytes(t *testing.T) {
	image := QRImage{MediaType: "image/svg+xml", Content: "<svg/>"}
	if b, err := image.Bytes(); err != nil || string(b) != "<svg/>" {
		t.Fatalf("SVG bytes = %q, %v", b, err)
	}
	for _, image := range []QRImage{{MediaType: "image/png", Content: "!"}, {MediaType: "image/unknown"}} {
		if _, err := image.Bytes(); err == nil {
			t.Fatal("expected error for malformed or unsupported image")
		}
	}
}
