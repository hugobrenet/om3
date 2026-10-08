package ai

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	agentURLEnv             = "OPENSVC_AI_AGENT_URL"
	agentCAFileEnv          = "OPENSVC_AI_AGENT_CA_FILE"
	maxCAFileBytes          = 1 << 20
	maxErrorBodyBytes       = 64 << 10
	maxErrorCodeRunes       = 128
	maxErrorMessageRunes    = 2048
	requestIDResponseHeader = "X-Request-ID"
	clusterIDRequestHeader  = "X-OpenSVC-Cluster-ID"
	maxClusterIDBytes       = 256
)

// Credential authenticates agent requests: a daemon-issued access token and
// the ID of the cluster whose daemon issued it. The agent forwards both; the
// cluster ID only selects where the token is verified.
type Credential struct {
	Token     string
	ClusterID string
}

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
	// The agent runs in the operator's infrastructure: there is no default.
	baseURL := strings.TrimSpace(os.Getenv(agentURLEnv))
	if baseURL == "" {
		return nil, fmt.Errorf("%s is not set: configure the HTTPS address of the AI agent", agentURLEnv)
	}
	return newHTTPSClient(baseURL, strings.TrimSpace(os.Getenv(agentCAFileEnv)))
}

func newHTTPSClient(baseURL string, caFile string) (*Client, error) {
	parsed, err := parseAgentURL(baseURL)
	if err != nil {
		return nil, err
	}
	roots, err := loadAgentCA(caFile)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	httpClient := &http.Client{Transport: agentOriginTransport{
		base:   transport,
		origin: parsed.Scheme + "://" + parsed.Host,
	}}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: parsed, httpClient: httpClient}, nil
}

func parseAgentURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return nil, fmt.Errorf("ai agent URL must be an absolute HTTPS URL")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, fmt.Errorf("ai agent URL must not contain a path, credentials, query or fragment")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("ai agent URL port must be between 1 and 65535")
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return nil, fmt.Errorf("ai agent URL port is empty")
	}
	parsed.Path = ""
	return parsed, nil
}

// An explicit CA bundle replaces system roots; an empty path uses system roots.
func loadAgentCA(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, nil
	}
	path := filepath.Clean(caFile)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return nil, fmt.Errorf("ai agent CA file must be an absolute file path")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ai agent CA file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCAFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read ai agent CA file: %w", err)
	}
	roots := x509.NewCertPool()
	if len(data) > maxCAFileBytes || !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("ai agent CA file must contain PEM certificates and be at most 1 MiB")
	}
	return roots, nil
}

type agentOriginTransport struct {
	base   http.RoundTripper
	origin string
}

func (t agentOriginTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host != t.origin || (request.Host != "" && request.Host != request.URL.Host) {
		return nil, fmt.Errorf("ai agent request destination differs from configured HTTPS origin")
	}
	return t.base.RoundTrip(request)
}

func (c *Client) endpoint(path string) string {
	endpoint := *c.baseURL
	endpoint.Path = path
	return endpoint.String()
}

func (c *Client) newAuthenticatedRequest(ctx context.Context, method string, path string, cred Credential, body io.Reader) (*http.Request, error) {
	if strings.TrimSpace(cred.Token) == "" {
		return nil, fmt.Errorf("ai agent Bearer token is empty")
	}
	if err := validateClusterID(cred.ClusterID); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, fmt.Errorf("create ai agent request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+cred.Token)
	request.Header.Set(clusterIDRequestHeader, cred.ClusterID)
	return request, nil
}

func validateClusterID(id string) error {
	if id == "" || len(id) > maxClusterIDBytes || strings.TrimSpace(id) != id {
		return fmt.Errorf("ai agent cluster ID must contain 1 to %d bytes without surrounding whitespace", maxClusterIDBytes)
	}
	for _, r := range id {
		if r == ',' || unicode.IsControl(r) {
			return fmt.Errorf("ai agent cluster ID contains a comma or control character")
		}
	}
	return nil
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
