package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode"
)

const (
	defaultBaseURL          = "http://127.0.0.1:8090"
	baseURLEnv              = "OPENSVC_AI_AGENT_URL"
	maxErrorBodyBytes       = 64 << 10
	maxErrorCodeRunes       = 128
	maxErrorMessageRunes    = 2048
	requestIDResponseHeader = "X-Request-ID"
)

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("ai agent returned HTTP %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.Code != "" {
		message += ": " + e.Code
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	if e.RequestID != "" {
		message += " (request_id=" + e.RequestID + ")"
	}
	return message
}

func New() (*Client, error) {
	baseURL := strings.TrimSpace(os.Getenv(baseURLEnv))
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return newClient(baseURL, nil)
}

func newClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ai agent base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("ai agent base URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("ai agent base URL host is empty")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("ai agent base URL must not contain a path, credentials, query, or fragment")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("ai agent base URL must use a loopback IP")
	}
	parsed.Path = ""
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: parsed, httpClient: httpClient}, nil
}

func (c *Client) endpoint(path string) string {
	endpoint := *c.baseURL
	endpoint.Path = path
	return endpoint.String()
}

func (c *Client) newAuthenticatedRequest(ctx context.Context, method string, path string, token string, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("ai agent Bearer token is empty")
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, fmt.Errorf("create ai agent request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return request, nil
}

func decodeAPIError(response *http.Response, requestID string, token string) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	if err != nil || len(body) > maxErrorBodyBytes {
		return &APIError{StatusCode: response.StatusCode, RequestID: requestID}
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &APIError{StatusCode: response.StatusCode, RequestID: requestID}
	}
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       redactSecret(normalizeErrorText(payload.Error.Code, maxErrorCodeRunes), token),
		Message:    redactSecret(normalizeErrorText(payload.Error.Message, maxErrorMessageRunes), token),
		RequestID:  requestID,
	}
}

func normalizeErrorText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func redactSecret(value string, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[redacted]")
}
