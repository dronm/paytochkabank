package paytochkabank

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientAuthorizationAndBasePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandbox/v2/test" || r.URL.Query().Get("q") != "a/b + c" {
			t.Errorf("unexpected URL %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing headers")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"Data":{"amount":1}}` {
			t.Errorf("body %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"Data":{"id":"ok"},"FutureField":true}`)
	}))
	defer server.Close()
	client, err := NewClient("test-token", WithBaseURL(server.URL+"/sandbox/v2/"))
	if err != nil {
		t.Fatal(err)
	}
	var out dataEnvelope[struct {
		ID string `json:"id"`
	}]
	err = client.do(context.Background(), http.MethodPost, "/test", url.Values{"q": {"a/b + c"}}, dataEnvelope[map[string]int]{Data: map[string]int{"amount": 1}}, &out)
	if err != nil || out.Data.ID != "ok" {
		t.Fatalf("response = %+v, %v", out, err)
	}
}

func TestClientRejectsConfiguration(t *testing.T) {
	for _, token := range []string{"", " token", "a\nb", "a\x00b", "a\x7fb"} {
		if _, err := NewClient(token); err == nil {
			t.Errorf("accepted token %q", token)
		}
	}
	for _, option := range []Option{nil, WithHTTPClient(nil), WithResponseLimit(0), WithBaseURL("relative"), WithBaseURL("http://example.com"), WithBaseURL("https://user:pass@example.com"), WithBaseURL("https://example.com?token=x")} {
		if _, err := NewClient("test", option); err == nil {
			t.Errorf("accepted bad option")
		}
	}
}

func TestClientDoesNotRetryUnknownWrite(t *testing.T) {
	var calls atomic.Int32
	sentinel := errors.New("connection lost")
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, sentinel
	})
	client, err := NewClient("test", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	err = client.do(context.Background(), http.MethodPost, "/write", nil, map[string]int{"x": 1}, nil)
	if calls.Load() != 1 || !errors.Is(err, sentinel) || !IsOutcomeUnknown(err) {
		t.Fatalf("calls=%d, error=%v", calls.Load(), err)
	}
	err = client.do(context.Background(), http.MethodGet, "/read", nil, nil, nil)
	if IsOutcomeUnknown(err) {
		t.Fatal("read classified as uncertain write")
	}
}

func TestClientCanceledBeforeSendIsNotUnknown(t *testing.T) {
	client, _ := NewClient("test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.do(ctx, http.MethodPost, "/write", nil, nil, nil)
	if !errors.Is(err, context.Canceled) || IsOutcomeUnknown(err) {
		t.Fatalf("error %v", err)
	}
}

func TestClientNeverFollowsRedirectsOrMutatesSuppliedClient(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/another-write")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	supplied := &http.Client{Timeout: time.Second}
	client, _ := NewClient("test", WithHTTPClient(supplied), WithBaseURL(server.URL))
	err := client.do(context.Background(), http.MethodPost, "/write", nil, map[string]int{"x": 1}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 307 || calls.Load() != 1 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
	if supplied.CheckRedirect != nil {
		t.Fatal("caller client was mutated")
	}
}

func TestClientErrorsAndUnknownOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		limit   int64
		unknown bool
	}{
		{"rejection", 403, `{"errorCode":"Forbidden","message":"permission denied"}`, 2048, false},
		{"server error", 500, `{"errorCode":"Internal","message":"try later"}`, 2048, true},
		{"dependency error", 424, `{"message":"upstream failure"}`, 2048, true},
		{"redirect", 307, `{"message":"redirect"}`, 2048, true},
		{"invalid success", 200, `not JSON`, 2048, true},
		{"missing Data", 200, `{}`, 2048, true},
		{"null Data", 200, `{"Data":null}`, 2048, true},
		{"truncated size", 200, `{"Data":{"x":"very long response"}}`, 12, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client, _ := NewClient("test", WithBaseURL(server.URL), WithResponseLimit(tt.limit))
			var out dataEnvelope[map[string]any]
			err := client.do(context.Background(), http.MethodPost, "/write", nil, nil, &out)
			if err == nil || IsOutcomeUnknown(err) != tt.unknown {
				t.Fatalf("error=%v unknown=%t", err, IsOutcomeUnknown(err))
			}
			if tt.status == 403 {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "Forbidden" || apiErr.Message != "permission denied" {
					t.Fatalf("API error=%+v", apiErr)
				}
			}
		})
	}
}

func TestClientPropagatesContextDeadline(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client, _ := NewClient("test", WithHTTPClient(&http.Client{Transport: transport}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := client.do(ctx, http.MethodPost, "/write", nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !IsOutcomeUnknown(err) {
		t.Fatalf("error %v", err)
	}
}

func TestValidationRejectsPathInjection(t *testing.T) {
	for _, input := range []string{"", "..", "a/b", "a?b", "a#b", "%2e%2e", "a b", "a\nb"} {
		if validatePathID("id", input) == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if err := validateOptionalURL("url", "https://shop.example/return?order=1"); err != nil {
		t.Fatal(err)
	}
	if err := validateOptionalURL("url", "javascript:alert(1)"); err == nil {
		t.Fatal("accepted non-http URL")
	}
}
