package paytochkabank

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// WebhookPublicKeyURL is Tochka's public signing-key endpoint. Fetch and refresh
// this key using trusted application configuration; token headers never select
// a key or a URL. ParseWebhookPublicKey accepts the returned single JWK.
const WebhookPublicKeyURL = "https://enter.tochka.com/doc/openapi/static/keys/public"

const (
	WebhookAcquiringInternetPayment = "acquiringInternetPayment"
	WebhookIncomingSBPPayment       = "incomingSbpPayment"
	maxWebhookBytes                 = 1 << 20
)

var (
	ErrInvalidWebhook          = errors.New("paytochkabank: invalid webhook")
	ErrInvalidWebhookSignature = errors.New("paytochkabank: invalid webhook signature")
	ErrWebhookExpired          = errors.New("paytochkabank: webhook expired")
	ErrWebhookNotYetValid      = errors.New("paytochkabank: webhook not yet valid")
	ErrWebhookType             = errors.New("paytochkabank: unexpected webhook type")
)

// WebhookVerifier verifies raw text/plain JWTs using a trusted RSA key. It is
// safe for concurrent use. Verification does not authorize an order transition:
// the application must also check the company, merchant, amount and payment ID.
type WebhookVerifier struct {
	key *rsa.PublicKey
}

// NewWebhookVerifier pins a copy of publicKey. Rotate keys by constructing a new
// verifier. A minimum of 2048 RSA bits is required.
func NewWebhookVerifier(publicKey *rsa.PublicKey) (*WebhookVerifier, error) {
	if err := validateWebhookKey(publicKey); err != nil {
		return nil, err
	}
	return &WebhookVerifier{key: &rsa.PublicKey{
		N: new(big.Int).Set(publicKey.N),
		E: publicKey.E,
	}}, nil
}

// ParseWebhookPublicKey parses a PEM PKIX/PKCS#1 RSA public key or a single RSA
// JWK. Private keys, certificates and multi-key JWKS documents are not accepted.
// The source of these bytes must be trusted independently of the webhook.
func ParseWebhookPublicKey(data []byte) (*rsa.PublicKey, error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return nil, fmt.Errorf("%w: invalid public-key size", ErrInvalidWebhook)
	}
	if block, rest := pem.Decode(data); block != nil {
		if len(bytes.TrimSpace(rest)) != 0 {
			return nil, fmt.Errorf("%w: trailing PEM data", ErrInvalidWebhook)
		}
		var key *rsa.PublicKey
		switch block.Type {
		case "PUBLIC KEY":
			value, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid PKIX public key", ErrInvalidWebhook)
			}
			var ok bool
			key, ok = value.(*rsa.PublicKey)
			if !ok {
				return nil, fmt.Errorf("%w: public key must be RSA", ErrInvalidWebhook)
			}
		case "RSA PUBLIC KEY":
			var err error
			key, err = x509.ParsePKCS1PublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid PKCS#1 public key", ErrInvalidWebhook)
			}
		default:
			return nil, fmt.Errorf("%w: unsupported PEM block", ErrInvalidWebhook)
		}
		if err := validateWebhookKey(key); err != nil {
			return nil, err
		}
		return key, nil
	}
	var jwk struct {
		KTY    string   `json:"kty"`
		N      string   `json:"n"`
		E      string   `json:"e"`
		Alg    string   `json:"alg"`
		Use    string   `json:"use"`
		KeyOps []string `json:"key_ops"`
		D      string   `json:"d"`
	}
	if err := json.Unmarshal(data, &jwk); err != nil || jwk.KTY != "RSA" || jwk.N == "" || jwk.E == "" || jwk.D != "" {
		return nil, fmt.Errorf("%w: expected a single public RSA JWK", ErrInvalidWebhook)
	}
	if (jwk.Alg != "" && jwk.Alg != "RS256") || (jwk.Use != "" && jwk.Use != "sig") {
		return nil, fmt.Errorf("%w: key is not for RS256 verification", ErrInvalidWebhook)
	}
	if len(jwk.KeyOps) != 0 {
		verify := false
		for _, op := range jwk.KeyOps {
			verify = verify || op == "verify"
		}
		if !verify {
			return nil, fmt.Errorf("%w: JWK does not permit verification", ErrInvalidWebhook)
		}
	}
	n, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil || len(n) == 0 || len(n) > 1024 {
		return nil, fmt.Errorf("%w: invalid JWK modulus", ErrInvalidWebhook)
	}
	e, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, fmt.Errorf("%w: invalid JWK exponent", ErrInvalidWebhook)
	}
	exponent := new(big.Int).SetBytes(e).Int64()
	if exponent > 1<<31-1 {
		return nil, fmt.Errorf("%w: JWK exponent too large", ErrInvalidWebhook)
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}
	if err := validateWebhookKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

func validateWebhookKey(key *rsa.PublicKey) error {
	if key == nil || key.N == nil || key.N.Sign() <= 0 || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.E < 3 || key.E%2 == 0 || key.E > 1<<31-1 {
		return fmt.Errorf("%w: expected a 2048..8192-bit RSA public key", ErrInvalidWebhook)
	}
	return nil
}

// WebhookEvent contains the original verified JSON payload. Unknown webhook
// types are preserved for forward compatibility. Treat Payload as immutable.
type WebhookEvent struct {
	WebhookType string
	Payload     json.RawMessage
}

// Verify verifies the signature before decoding the payload. Only RS256 is
// supported. exp and nbf are validated when present; Tochka examples omit them,
// so their absence is allowed. This does not provide replay protection.
func (v *WebhookVerifier) Verify(rawJWT []byte) (*WebhookEvent, error) {
	if v == nil || v.key == nil {
		return nil, fmt.Errorf("%w: verifier is not initialized", ErrInvalidWebhook)
	}
	if len(rawJWT) == 0 || len(rawJWT) > maxWebhookBytes {
		return nil, fmt.Errorf("%w: invalid token size", ErrInvalidWebhook)
	}
	parts := strings.Split(strings.TrimSpace(string(rawJWT)), ".")
	if len(parts) != 3 || len(parts[0]) > 4096 {
		return nil, fmt.Errorf("%w: expected compact JWS", ErrInvalidWebhook)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid header encoding", ErrInvalidWebhook)
	}
	var header struct {
		Alg  string          `json:"alg"`
		Crit json.RawMessage `json:"crit"`
		B64  *bool           `json:"b64"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Alg != "RS256" || len(header.Crit) != 0 || (header.B64 != nil && !*header.B64) {
		return nil, fmt.Errorf("%w: unsupported JWS header", ErrInvalidWebhook)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != v.key.Size() {
		return nil, ErrInvalidWebhookSignature
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(v.key, crypto.SHA256, hash[:], signature); err != nil {
		return nil, ErrInvalidWebhookSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid payload encoding", ErrInvalidWebhook)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil || claims == nil {
		return nil, fmt.Errorf("%w: expected JSON object payload", ErrInvalidWebhook)
	}
	var webhookType string
	if err := json.Unmarshal(claims["webhookType"], &webhookType); err != nil || strings.TrimSpace(webhookType) == "" {
		return nil, fmt.Errorf("%w: missing webhookType", ErrInvalidWebhook)
	}
	now := time.Now()
	if err := validateWebhookDates(claims, now); err != nil {
		return nil, err
	}
	return &WebhookEvent{WebhookType: webhookType, Payload: json.RawMessage(payload)}, nil
}

func validateWebhookDates(claims map[string]json.RawMessage, now time.Time) error {
	current := new(big.Rat).SetInt64(now.Unix())
	current.Add(current, new(big.Rat).SetFrac64(int64(now.Nanosecond()), 1e9))
	for _, name := range []string{"exp", "nbf"} {
		raw, ok := claims[name]
		if !ok {
			continue
		}
		var number json.Number
		if len(raw) == 0 || len(raw) > 64 || raw[0] == '"' || string(raw) == "null" || json.Unmarshal(raw, &number) != nil {
			return fmt.Errorf("%w: %s must be a NumericDate", ErrInvalidWebhook, name)
		}
		if i := strings.IndexAny(number.String(), "eE"); i >= 0 {
			exponent, err := strconv.Atoi(number.String()[i+1:])
			if err != nil || exponent < -30 || exponent > 30 {
				return fmt.Errorf("%w: %s exponent out of range", ErrInvalidWebhook, name)
			}
		}
		value, ok := new(big.Rat).SetString(number.String())
		if !ok {
			return fmt.Errorf("%w: invalid %s", ErrInvalidWebhook, name)
		}
		if name == "exp" && current.Cmp(value) >= 0 {
			return ErrWebhookExpired
		}
		if name == "nbf" && current.Cmp(value) < 0 {
			return ErrWebhookNotYetValid
		}
	}
	return nil
}

// Decode decodes a verified payload without imposing a schema on future events.
func (e *WebhookEvent) Decode(destination any) error {
	if e == nil {
		return fmt.Errorf("%w: nil event", ErrInvalidWebhook)
	}
	return json.Unmarshal(e.Payload, destination)
}

// AcquiringPaymentWebhook identifies a payment-link operation. Amount is
// normalized to kopecks. Preserve Status and PaymentType as bank-native values.
type AcquiringPaymentWebhook struct {
	WebhookType   string `json:"webhookType"`
	CustomerCode  string `json:"customerCode"`
	MerchantID    string `json:"merchantId"`
	OperationID   string `json:"operationId"`
	PaymentLinkID string `json:"paymentLinkId"`
	PaymentType   string `json:"paymentType"`
	Status        string `json:"status"`
	Amount        Money  `json:"amount"`
}

// AcquiringPayment returns typed fields only for an acquiring event. Missing
// optional bank fields remain zero-valued; the application validates correlation.
func (e *WebhookEvent) AcquiringPayment() (*AcquiringPaymentWebhook, error) {
	if e == nil || e.WebhookType != WebhookAcquiringInternetPayment {
		return nil, ErrWebhookType
	}
	type plain AcquiringPaymentWebhook
	var wire struct {
		plain
		Amount rubleMoney `json:"amount"`
	}
	if err := e.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: acquiring payload: %v", ErrInvalidWebhook, err)
	}
	payment := AcquiringPaymentWebhook(wire.plain)
	payment.Amount = Money(wire.Amount)
	return &payment, nil
}

// SBPPaymentWebhook contains the original identifiers needed for a direct SBP
// refund. OperationID is the NSPK trxId for sbpPayment. PaymentType may also be
// drPayment (digital ruble), for which API refunds are unavailable.
type SBPPaymentWebhook struct {
	WebhookType       string `json:"webhookType"`
	CustomerCode      string `json:"customerCode"`
	MerchantID        string `json:"merchantId"`
	QRCID             string `json:"qrcId"`
	OperationID       string `json:"operationId"`
	RefTransactionID  string `json:"refTransactionId"`
	PaymentType       string `json:"paymentType"`
	Amount            Money  `json:"amount"`
	Purpose           string `json:"purpose"`
	BrandName         string `json:"brandName"`
	PayerName         string `json:"payerName"`
	PayerMobileNumber string `json:"payerMobileNumber"`
}

// SBPPayment returns typed fields only for an incoming SBP event.
func (e *WebhookEvent) SBPPayment() (*SBPPaymentWebhook, error) {
	if e == nil || e.WebhookType != WebhookIncomingSBPPayment {
		return nil, ErrWebhookType
	}
	type plain SBPPaymentWebhook
	var wire struct {
		plain
		Amount rubleMoney `json:"amount"`
	}
	if err := e.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: SBP payload: %v", ErrInvalidWebhook, err)
	}
	payment := SBPPaymentWebhook(wire.plain)
	payment.Amount = Money(wire.Amount)
	return &payment, nil
}
