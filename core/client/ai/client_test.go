package ai

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
)

func newTestClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ai agent test base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("ai agent test base URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("ai agent test base URL host is empty")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("ai agent test base URL must not contain a path, credentials, query, or fragment")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("ai agent test base URL must use a loopback IP")
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

func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{agentURLEnv, agentCAFileEnv} {
		t.Setenv(key, "")
	}
}

func TestNewUsesDefaultRemoteURL(t *testing.T) {
	clearAgentEnv(t)
	client, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if got := client.baseURL.String(); got != DefaultAgentURL {
		t.Fatalf("default URL = %q", got)
	}
	transport := client.httpClient.Transport.(agentOriginTransport).base.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("proxy enabled")
	}
	if transport.TLSClientConfig.RootCAs != nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("system trust not used or verification disabled")
	}
}

func TestNewUsesHTTPSURLFromEnvironment(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv(agentURLEnv, " https://agent.example.test:8090/ ")
	client, err := New()
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.newAuthenticatedRequest(t.Context(), http.MethodGet, "/health", "test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.URL.String(); got != "https://agent.example.test:8090/health" {
		t.Fatalf("request URL = %q", got)
	}
	if request.Header.Get("Authorization") != "Bearer test-token" {
		t.Fatal("missing bearer token")
	}
}

func TestNewRejectsInvalidHTTPSURLs(t *testing.T) {
	for _, value := range []string{
		"", "agent.example.test", "/run/agent.sock", "http://agent.example.test",
		"http://127.0.0.1:8090", "unix:///run/agent.sock", "ftp://agent.example.test",
		"https:///missing-host", "https://agent.example.test/v1/ask",
		"https://agent.example.test/%2f", "https://user:password@agent.example.test",
		"https://agent.example.test?token=secret", "https://agent.example.test?",
		"https://agent.example.test#fragment", "https://agent.example.test:0",
		"https://agent.example.test:65536", "https://agent.example.test:",
		"https://agent.example.test:port", "https://agent.\nexample.test",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := newHTTPSClient(value, ""); err == nil {
				t.Fatal("invalid URL succeeded")
			}
		})
	}
}

func TestNewAcceptsRemoteHTTPSURLs(t *testing.T) {
	for _, value := range []string{"https://agent.example.test", "https://agent.example.test:8090/", "https://192.0.2.1:8090", "https://[2001:db8::1]:8090"} {
		t.Run(value, func(t *testing.T) {
			client, err := newHTTPSClient(value, "")
			if err != nil {
				t.Fatal(err)
			}
			if client.baseURL.Path != "" {
				t.Fatal("base URL has a path")
			}
		})
	}
}

func TestHelperRejectsInvalidBaseURLs(t *testing.T) {
	for _, baseURL := range []string{
		"http://example.com",
		"https://example.com",
		"ftp://127.0.0.1",
		"http://127.0.0.1/v1/ask",
		"http://user:pass@127.0.0.1",
		"http://127.0.0.1?token=value",
	} {
		t.Run(baseURL, func(t *testing.T) {
			if _, err := newTestClient(baseURL, nil); err == nil {
				t.Fatal("invalid base URL succeeded")
			}
		})
	}
}
