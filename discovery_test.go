package paytochkabank

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDiscoveryReadOperations(t *testing.T) {
	merchant := `{"merchantId":"MF0000000001","legalId":"LF0000000001","brandName":"Кофейня","address":"Татарская, 11","city":"Москва","status":"Active","capabilities":"110"}`
	responses := map[string]string{
		"/open-banking/v1.0/customers":                     `{"Data":{"Customer":[{"customerCode":"300000092","customerType":"Business","taxCode":"7706812159","fullName":"ООО Кофейня","shortName":null}]}}`,
		"/sbp/v1.0/customer/300000092/044525104":           `{"Data":{"legalId":"LF0000000001","customerCode":"300000092","bankCode":"044525104","status":"Active","MerchantList":[` + merchant + `]}}`,
		"/sbp/v1.0/merchant/legal-entity/LF0000000001":     `{"Data":{"MerchantList":[` + merchant + `]}}`,
		"/acquiring/v1.0/retailers?customerCode=300000092": `{"Data":{"Retailer":[{"merchantId":"200000000001056","name":"Кофейня","url":null,"status":"REG","isActive":true,"paymentModes":["sbp","card"]}]}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test.jwt" || r.ContentLength != 0 {
			t.Errorf("unexpected discovery request %s %s", r.Method, r.URL)
		}
		body, ok := responses[r.URL.RequestURI()]
		if !ok {
			t.Errorf("unexpected discovery URL %s", r.URL)
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	client, err := NewClient("test.jwt", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	customers, err := client.ListCustomers(ctx)
	wantCustomers := []Customer{{CustomerCode: "300000092", CustomerType: "Business", TaxCode: "7706812159", FullName: "ООО Кофейня"}}
	if err != nil || !reflect.DeepEqual(customers, wantCustomers) {
		t.Fatalf("unexpected customers %+v, %v", customers, err)
	}

	wantMerchants := []SBPMerchant{{MerchantID: "MF0000000001", LegalID: "LF0000000001", BrandName: "Кофейня", Address: "Татарская, 11", City: "Москва", Status: "Active", Capabilities: "110"}}
	info, err := client.SBP.GetCustomerInfo(ctx, "300000092", "044525104")
	wantInfo := &SBPCustomerInfo{LegalID: "LF0000000001", CustomerCode: "300000092", BankCode: "044525104", Status: "Active", Merchants: wantMerchants}
	if err != nil || !reflect.DeepEqual(info, wantInfo) {
		t.Fatalf("unexpected SBP registration %+v, %v", info, err)
	}
	merchants, err := client.SBP.ListMerchants(ctx, info.LegalID)
	if err != nil || !reflect.DeepEqual(merchants, wantMerchants) {
		t.Fatalf("unexpected SBP merchants %+v, %v", merchants, err)
	}

	retailers, err := client.Acquiring.ListRetailers(ctx, "300000092")
	wantRetailers := []Retailer{{MerchantID: "200000000001056", Name: "Кофейня", Status: "REG", IsActive: true, PaymentModes: []string{"sbp", "card"}}}
	if err != nil || !reflect.DeepEqual(retailers, wantRetailers) {
		t.Fatalf("unexpected retailers %+v, %v", retailers, err)
	}
}

func TestDiscoveryDistinguishesEmptyAndMissingLists(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "empty", body: `{"Data":{"Customer":[]}}`},
		{name: "missing", body: `{"Data":{}}`, wantError: true},
		{name: "null", body: `{"Data":{"Customer":null}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := NewClient("test.jwt", WithBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			customers, err := client.ListCustomers(context.Background())
			if tc.wantError {
				var requestErr *RequestError
				if !errors.As(err, &requestErr) || requestErr.OutcomeUnknown {
					t.Fatalf("expected a read error, got %v", err)
				}
				return
			}
			if err != nil || customers == nil || len(customers) != 0 {
				t.Fatalf("expected an empty list, got %+v, %v", customers, err)
			}
		})
	}
}
