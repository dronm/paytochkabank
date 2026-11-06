package paytochkabank

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Customer is an organization or individual available to the bearer token.
type Customer struct {
	CustomerCode string `json:"customerCode"`
	CustomerType string `json:"customerType"`
	TaxCode      string `json:"taxCode"`
	FullName     string `json:"fullName"`
	ShortName    string `json:"shortName"`
}

// ListCustomers reads the companies and individuals visible to the token.
func (c *Client) ListCustomers(ctx context.Context) ([]Customer, error) {
	const path = "/open-banking/v1.0/customers"
	var response dataEnvelope[struct {
		Customers []Customer `json:"Customer"`
	}]
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, err
	}
	if response.Data.Customers == nil {
		return nil, &RequestError{Method: http.MethodGet, Path: path, Err: errors.New("response is missing Data.Customer")}
	}
	return response.Data.Customers, nil
}

// SBPCustomerInfo describes a customer's registration with the SBP service.
type SBPCustomerInfo struct {
	LegalID      string        `json:"legalId"`
	CustomerCode string        `json:"customerCode"`
	BankCode     string        `json:"bankCode"`
	Status       string        `json:"status"`
	Merchants    []SBPMerchant `json:"MerchantList"`
}

// SBPMerchant is a registered SBP outlet and its QR capabilities bit string.
type SBPMerchant struct {
	MerchantID   string `json:"merchantId"`
	LegalID      string `json:"legalId"`
	BrandName    string `json:"brandName"`
	Address      string `json:"address"`
	City         string `json:"city"`
	Status       string `json:"status"`
	Capabilities string `json:"capabilities"`
}

// GetCustomerInfo resolves a customer and bank code to its SBP registration.
func (service *SBPService) GetCustomerInfo(ctx context.Context, customerCode, bankCode string) (*SBPCustomerInfo, error) {
	if err := validatePathID("customerCode", customerCode); err != nil {
		return nil, err
	}
	if err := validatePathID("bankCode", bankCode); err != nil {
		return nil, err
	}
	path := "/sbp/v1.0/customer/" + customerCode + "/" + bankCode
	var response dataEnvelope[SBPCustomerInfo]
	if err := service.client.do(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, err
	}
	return &response.Data, nil
}

// ListMerchants reads the outlets registered to an SBP legal entity.
func (service *SBPService) ListMerchants(ctx context.Context, legalID string) ([]SBPMerchant, error) {
	if err := validatePathID("legalID", legalID); err != nil {
		return nil, err
	}
	path := "/sbp/v1.0/merchant/legal-entity/" + legalID
	var response dataEnvelope[struct {
		Merchants []SBPMerchant `json:"MerchantList"`
	}]
	if err := service.client.do(ctx, http.MethodGet, path, nil, nil, &response); err != nil {
		return nil, err
	}
	if response.Data.Merchants == nil {
		return nil, &RequestError{Method: http.MethodGet, Path: path, Err: errors.New("response is missing Data.MerchantList")}
	}
	return response.Data.Merchants, nil
}

// Retailer describes an internet-acquiring outlet for the selected customer.
type Retailer struct {
	MerchantID   string   `json:"merchantId"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Status       string   `json:"status"`
	IsActive     bool     `json:"isActive"`
	PaymentModes []string `json:"paymentModes"`
}

// ListRetailers reads the internet-acquiring outlets for a customer code.
func (service *AcquiringService) ListRetailers(ctx context.Context, customerCode string) ([]Retailer, error) {
	if err := validateRequired("customerCode", customerCode); err != nil {
		return nil, err
	}
	const path = "/acquiring/v1.0/retailers"
	var response dataEnvelope[struct {
		Retailers []Retailer `json:"Retailer"`
	}]
	query := url.Values{"customerCode": {customerCode}}
	if err := service.client.do(ctx, http.MethodGet, path, query, nil, &response); err != nil {
		return nil, err
	}
	if response.Data.Retailers == nil {
		return nil, &RequestError{Method: http.MethodGet, Path: path, Err: errors.New("response is missing Data.Retailer")}
	}
	return response.Data.Retailers, nil
}
