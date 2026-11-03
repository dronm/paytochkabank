package paytochkabank

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	webhookTestOnce sync.Once
	webhookTestRSA  *rsa.PrivateKey
	webhookTestErr  error
)

func webhookKeyForTest(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	webhookTestOnce.Do(func() {
		webhookTestRSA, webhookTestErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if webhookTestErr != nil {
		t.Fatal(webhookTestErr)
	}
	return webhookTestRSA
}

func signWebhookForTest(t *testing.T, key *rsa.PrivateKey, header, payload string) []byte {
	t.Helper()
	message := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	hash := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(message + "." + base64.RawURLEncoding.EncodeToString(signature))
}

func TestWebhookVerifyAndDecode(t *testing.T) {
	key := webhookKeyForTest(t)
	verifier, err := NewWebhookVerifier(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("SBP amount and refund identifiers", func(t *testing.T) {
		payload := `{"webhookType":"incomingSbpPayment","paymentType":"sbpPayment","amount":"0.33","qrcId":"qr-1","operationId":"nspk-trx","refTransactionId":"bank-trx","customerCode":"1234567ab","merchantId":"MF0000000001","futureField":{"retained":true}}`
		event, err := verifier.Verify(signWebhookForTest(t, key, `{"alg":"RS256","typ":"JWT"}`, payload))
		if err != nil {
			t.Fatal(err)
		}
		payment, err := event.SBPPayment()
		if err != nil {
			t.Fatal(err)
		}
		if payment.Amount != Money(33) || payment.QRCID != "qr-1" || payment.OperationID != "nspk-trx" || payment.RefTransactionID != "bank-trx" {
			t.Fatalf("unexpected payment: %+v", payment)
		}
		var original map[string]json.RawMessage
		if err := event.Decode(&original); err != nil || original["futureField"] == nil {
			t.Fatalf("verified payload not retained: %v", err)
		}
		if _, err := event.AcquiringPayment(); !errors.Is(err, ErrWebhookType) {
			t.Fatalf("wrong event accessor: %v", err)
		}
	})
	t.Run("acquiring numeric ruble amount", func(t *testing.T) {
		payload := `{"webhookType":"acquiringInternetPayment","amount":1234.56,"operationId":"operation-1","paymentLinkId":"order-7","status":"APPROVED","paymentType":"card"}`
		event, err := verifier.Verify(signWebhookForTest(t, key, `{"alg":"RS256"}`, payload))
		if err != nil {
			t.Fatal(err)
		}
		payment, err := event.AcquiringPayment()
		if err != nil || payment.Amount != Money(123456) || payment.OperationID != "operation-1" || payment.PaymentLinkID != "order-7" {
			t.Fatalf("unexpected acquiring payment: %+v, %v", payment, err)
		}
	})
	t.Run("future webhook type", func(t *testing.T) {
		event, err := verifier.Verify(signWebhookForTest(t, key, `{"alg":"RS256"}`, `{"webhookType":"futureEvent","detail":7}`))
		if err != nil || event.WebhookType != "futureEvent" {
			t.Fatalf("unknown event rejected: %+v, %v", event, err)
		}
	})
	t.Run("digital ruble retains original type", func(t *testing.T) {
		event, err := verifier.Verify(signWebhookForTest(t, key, `{"alg":"RS256"}`, `{"webhookType":"incomingSbpPayment","paymentType":"drPayment","amount":"100.01"}`))
		if err != nil {
			t.Fatal(err)
		}
		payment, err := event.SBPPayment()
		if err != nil || payment.PaymentType != "drPayment" || payment.Amount != 10001 {
			t.Fatalf("unexpected digital-ruble event: %+v, %v", payment, err)
		}
	})
}

func TestWebhookRejectsUntrustedInput(t *testing.T) {
	key := webhookKeyForTest(t)
	verifier, err := NewWebhookVerifier(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	validPayload := `{"webhookType":"incomingSbpPayment","amount":"1.00"}`
	tests := []struct {
		name    string
		header  string
		payload string
		want    error
	}{
		{"none algorithm", `{"alg":"none"}`, validPayload, ErrInvalidWebhook},
		{"algorithm confusion", `{"alg":"HS256"}`, validPayload, ErrInvalidWebhook},
		{"critical extension", `{"alg":"RS256","crit":["extension"]}`, validPayload, ErrInvalidWebhook},
		{"unencoded payload", `{"alg":"RS256","b64":false}`, validPayload, ErrInvalidWebhook},
		{"missing event type", `{"alg":"RS256"}`, `{"amount":"1.00"}`, ErrInvalidWebhook},
		{"non-object payload", `{"alg":"RS256"}`, `null`, ErrInvalidWebhook},
		{"malformed payload", `{"alg":"RS256"}`, `{`, ErrInvalidWebhook},
		{"expired", `{"alg":"RS256"}`, `{"webhookType":"incomingSbpPayment","exp":1}`, ErrWebhookExpired},
		{"not yet valid", `{"alg":"RS256"}`, fmt.Sprintf(`{"webhookType":"incomingSbpPayment","nbf":%d}`, time.Now().Add(24*time.Hour).Unix()), ErrWebhookNotYetValid},
		{"invalid date string", `{"alg":"RS256"}`, `{"webhookType":"incomingSbpPayment","exp":"9999999999"}`, ErrInvalidWebhook},
		{"invalid null date", `{"alg":"RS256"}`, `{"webhookType":"incomingSbpPayment","exp":null}`, ErrInvalidWebhook},
		{"excessive date exponent", `{"alg":"RS256"}`, `{"webhookType":"incomingSbpPayment","exp":1e999999999}`, ErrInvalidWebhook},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := verifier.Verify(signWebhookForTest(t, key, test.header, test.payload)); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
	t.Run("tampered payload", func(t *testing.T) {
		token := string(signWebhookForTest(t, key, `{"alg":"RS256"}`, validPayload))
		parts := strings.Split(token, ".")
		parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"webhookType":"incomingSbpPayment","amount":"999.00"}`))
		if _, err := verifier.Verify([]byte(strings.Join(parts, "."))); !errors.Is(err, ErrInvalidWebhookSignature) {
			t.Fatalf("tampered token accepted: %v", err)
		}
	})
	t.Run("wrong trusted key", func(t *testing.T) {
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(signWebhookForTest(t, otherKey, `{"alg":"RS256"}`, validPayload)); !errors.Is(err, ErrInvalidWebhookSignature) {
			t.Fatalf("wrong-key token accepted: %v", err)
		}
	})
	t.Run("size and token shape", func(t *testing.T) {
		for _, raw := range [][]byte{nil, []byte("bad.jwt"), []byte("bad.jwt.signature"), []byte(strings.Repeat("a", maxWebhookBytes+1))} {
			if _, err := verifier.Verify(raw); err == nil {
				t.Fatal("invalid token accepted")
			}
		}
	})
	t.Run("header URL cannot replace pinned key", func(t *testing.T) {
		if _, err := verifier.Verify(signWebhookForTest(t, key, `{"alg":"RS256","jku":"https://untrusted.invalid/keys"}`, validPayload)); err != nil {
			t.Fatalf("URL should be ignored, pinned key should verify: %v", err)
		}
	})
}

func TestWebhookPublicKeyFormats(t *testing.T) {
	key := webhookKeyForTest(t)
	pkix, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	jwk, err := json.Marshal(map[string]string{
		"kty": "RSA", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"PKIX":  pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkix}),
		"PKCS1": pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}),
		"JWK":   jwk,
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := ParseWebhookPublicKey(data)
			if err != nil || parsed.E != key.E || parsed.N.Cmp(key.N) != 0 {
				t.Fatalf("unexpected parsed key: %v", err)
			}
		})
	}
	for _, data := range [][]byte{nil, []byte(`{"keys":[]}`), []byte(`{"kty":"EC","n":"x","e":"y"}`), []byte(`{"kty":"RSA","n":"AQ","e":"Aw"}`)} {
		if _, err := ParseWebhookPublicKey(data); err == nil {
			t.Fatal("invalid or undersized key accepted")
		}
	}
	if _, err := NewWebhookVerifier(nil); err == nil {
		t.Fatal("nil key accepted")
	}
	if _, err := NewWebhookVerifier(&rsa.PublicKey{N: big.NewInt(123), E: 3}); err == nil {
		t.Fatal("undersized key accepted")
	}
	if _, err := (&WebhookVerifier{}).Verify([]byte("a.b.c")); err == nil {
		t.Fatal("uninitialized verifier accepted")
	}
}

func TestWebhookVerifierCopiesTrustedKey(t *testing.T) {
	key := webhookKeyForTest(t)
	publicKey := &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
	verifier, err := NewWebhookVerifier(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey.N.SetInt64(1)
	if _, err := verifier.Verify(signWebhookForTest(t, key, `{"alg":"RS256"}`, `{"webhookType":"futureEvent"}`)); err != nil {
		t.Fatalf("caller mutation changed verifier: %v", err)
	}
}

func TestWebhookPublicPayloadJSONRoundTrip(t *testing.T) {
	t.Run("acquiring", func(t *testing.T) {
		original := AcquiringPaymentWebhook{
			WebhookType: WebhookAcquiringInternetPayment,
			Amount:      Money(123456),
			OperationID: "operation-1",
			Status:      "APPROVED",
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var result AcquiringPaymentWebhook
		if err := json.Unmarshal(data, &result); err != nil || !reflect.DeepEqual(original, result) {
			t.Fatalf("public value did not round-trip: %+v, %v", result, err)
		}
	})
	t.Run("SBP", func(t *testing.T) {
		original := SBPPaymentWebhook{
			WebhookType: WebhookIncomingSBPPayment,
			Amount:      Money(123456),
			QRCID:       "qr-1",
			OperationID: "nspk-trx",
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var result SBPPaymentWebhook
		if err := json.Unmarshal(data, &result); err != nil || !reflect.DeepEqual(original, result) {
			t.Fatalf("public value did not round-trip: %+v, %v", result, err)
		}
	})
}
