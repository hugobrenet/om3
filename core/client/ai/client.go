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
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	defaultSocketPath       = "/run/opensvc-ai-agent/agent.sock"
	unixBaseURL             = "http://127.0.0.1"
	socketPathEnv           = "OPENSVC_AI_AGENT_SOCKET"
	maximumUnixPathBytes    = 107
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
	socketPath := strings.TrimSpace(os.Getenv(socketPathEnv))
	if socketPath == "" {
		socketPath = defaultSocketPath
	}
	return newUnixClient(socketPath)
}

func newUnixClient(socketPath string) (*Client, error) {
	path, err := cleanUnixSocketPath(socketPath)
	if err != nil {
		return nil, fmt.Errorf("parse ai agent Unix socket path: %w", err)
	}
	parsed, err := url.Parse(unixBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse internal ai agent URL: %w", err)
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}
	httpClient := &http.Client{Transport: transport}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: parsed, httpClient: httpClient}, nil
}

func cleanUnixSocketPath(value string) (string, error) {
	path := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	if path == string(filepath.Separator) {
		return "", fmt.Errorf("path must name a socket")
	}
	if len([]byte(path)) > maximumUnixPathBytes {
		return "", fmt.Errorf("path exceeds the Linux Unix socket limit of %d bytes", maximumUnixPathBytes)
	}
	return path, nil
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
