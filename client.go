package paytochkabank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ProductionURL is the base of Tochka Bank's live API.
	ProductionURL = "https://enter.tochka.com/uapi"
	// SandboxURL serves fixed examples; it is not a payment lifecycle simulator.
	SandboxURL = "https://enter.tochka.com/sandbox/v2"
	// SandboxToken is the public token documented for the fixed-response sandbox.
	SandboxToken               = "sandbox.jwt.token"
	defaultResponseLimit int64 = 2 << 20
)

// Client is safe for concurrent calls after construction. Its service fields
// must not be reassigned while in use. Tokens and transport options are immutable.
type Client struct {
	Acquiring     *AcquiringService
	SBP           *SBPService
	baseURL       *url.URL
	token         string
	httpClient    *http.Client
	responseLimit int64
}

type clientOptions struct {
	baseURL       string
	httpClient    *http.Client
	responseLimit int64
}

// Option configures a new client.
type Option func(*clientOptions) error

// WithBaseURL changes the base URL. HTTP is allowed for local test servers only.
func WithBaseURL(baseURL string) Option {
	return func(o *clientOptions) error {
		o.baseURL = baseURL
		return nil
	}
}

// WithHTTPClient supplies a transport and timeouts. A shallow copy is made and
// redirects are disabled; the caller's client is not mutated. Custom transports
// must not retry non-idempotent requests or log Authorization headers.
func WithHTTPClient(client *http.Client) Option {
	return func(o *clientOptions) error {
		if client == nil {
			return &ValidationError{Field: "HTTPClient", Message: "must not be nil"}
		}
		o.httpClient = client
		return nil
	}
}

// WithResponseLimit changes the maximum response body size (default 2 MiB).
func WithResponseLimit(bytes int64) Option {
	return func(o *clientOptions) error {
		if bytes < 1 || bytes > 64<<20 {
			return &ValidationError{Field: "ResponseLimit", Message: "must be between 1 byte and 64 MiB"}
		}
		o.responseLimit = bytes
		return nil
	}
}

// NewClient constructs a client using a JWT bearer token. Obtain the company,
// account and merchant IDs from internet banking or the bank's setup APIs.
func NewClient(token string, options ...Option) (*Client, error) {
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
		return nil, &ValidationError{Field: "Token", Message: "must be a nonempty bearer token without whitespace"}
	}
	o := clientOptions{baseURL: ProductionURL, httpClient: &http.Client{Timeout: 30 * time.Second}, responseLimit: defaultResponseLimit}
	for _, option := range options {
		if option == nil {
			return nil, &ValidationError{Field: "Option", Message: "must not be nil"}
		}
		if err := option(&o); err != nil {
			return nil, err
		}
	}
	u, err := url.Parse(o.baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, &ValidationError{Field: "BaseURL", Message: "must be an absolute HTTP(S) base URL without credentials, query or fragment"}
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, &ValidationError{Field: "BaseURL", Message: "HTTPS is required outside localhost"}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	transport := *o.httpClient
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{baseURL: u, token: token, httpClient: &transport, responseLimit: o.responseLimit}
	c.Acquiring = &AcquiringService{client: c}
	c.SBP = &SBPService{client: c}
	return c, nil
}

type dataEnvelope[T any] struct {
	Data T `json:"Data"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	if ctx == nil {
		return &ValidationError{Field: "Context", Message: "must not be nil"}
	}
	if err := ctx.Err(); err != nil {
		return &RequestError{Method: method, Path: path, Err: err}
	}
	u := *c.baseURL
	u.Path += "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	u.RawQuery = query.Encode()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return &RequestError{Method: method, Path: path, Err: fmt.Errorf("encode request: %w", err)}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(encoded))
	if err != nil {
		return &RequestError{Method: method, Path: path, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "paytochkabank/0.1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	write := method != http.MethodGet && method != http.MethodHead
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &RequestError{Method: method, Path: path, Err: err, OutcomeUnknown: write}
	}
	defer resp.Body.Close()
	uncertain := write && (resp.StatusCode >= 500 || resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusFailedDependency)
	response, readErr := io.ReadAll(io.LimitReader(resp.Body, c.responseLimit+1))
	if readErr != nil {
		return &RequestError{Method: method, Path: path, Err: fmt.Errorf("read response: %w", readErr), OutcomeUnknown: write && (uncertain || resp.StatusCode >= 200 && resp.StatusCode < 300)}
	}
	if int64(len(response)) > c.responseLimit {
		return &RequestError{Method: method, Path: path, Err: errors.New("response exceeds configured size limit"), OutcomeUnknown: write && (uncertain || resp.StatusCode >= 200 && resp.StatusCode < 300)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := &APIError{StatusCode: resp.StatusCode, Body: response, OutcomeUnknown: uncertain, Message: http.StatusText(resp.StatusCode)}
		var fields struct {
			Code      string `json:"code"`
			ErrorCode string `json:"errorCode"`
			Message   string `json:"message"`
		}
		if json.Unmarshal(response, &fields) == nil {
			e.Code = fields.ErrorCode
			if e.Code == "" {
				e.Code = fields.Code
			}
			if fields.Message != "" {
				e.Message = fields.Message
			}
		}
		return e
	}
	if out == nil {
		return nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response, &envelope); err != nil {
		return &RequestError{Method: method, Path: path, Err: fmt.Errorf("decode response: %w", err), OutcomeUnknown: write}
	}
	if data, ok := envelope["Data"]; !ok || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return &RequestError{Method: method, Path: path, Err: errors.New("response is missing non-null Data"), OutcomeUnknown: write}
	}
	if err := json.Unmarshal(response, out); err != nil {
		return &RequestError{Method: method, Path: path, Err: fmt.Errorf("decode response: %w", err), OutcomeUnknown: write}
	}
	return nil
}
