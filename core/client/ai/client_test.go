package ai

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestNewUsesDefaultUnixSocket(t *testing.T) {
	t.Setenv(socketPathEnv, "")
	client, err := New()
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if got := client.baseURL.String(); got != unixBaseURL {
		t.Fatalf("base URL = %q, want %q", got, unixBaseURL)
	}
}

func TestNewUsesUnixSocketFromEnvironment(t *testing.T) {
	t.Setenv(socketPathEnv, " /run/opensvc-ai-agent/../opensvc-ai-agent/custom.sock ")
	client, err := New()
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := client.newAuthenticatedRequest(t.Context(), http.MethodGet, "/health", "token", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if got := request.URL.String(); got != unixBaseURL+"/health" {
		t.Fatalf("request URL = %q", got)
	}
}

func TestNewRejectsInvalidUnixSocketPaths(t *testing.T) {
	for _, socketPath := range []string{"agent.sock", "/", "/" + strings.Repeat("a", maximumUnixPathBytes)} {
		t.Run(socketPath, func(t *testing.T) {
			if _, err := newUnixClient(socketPath); err == nil {
				t.Fatal("invalid Unix socket path succeeded")
			}
		})
	}
}

func TestUnixClientSendsAuthenticatedHTTPRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(response, `{"status":"ok"}`)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	client, err := newUnixClient(path)
	if err != nil {
		t.Fatalf("new Unix client: %v", err)
	}
	request, err := client.newAuthenticatedRequest(t.Context(), http.MethodGet, "/health", "test-token", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		t.Fatalf("request over Unix socket: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", response.Status)
	}
}

func TestUnixClientMissingSocketErrorDoesNotExposeToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")
	client, err := newUnixClient(path)
	if err != nil {
		t.Fatalf("new Unix client: %v", err)
	}
	request, err := client.newAuthenticatedRequest(t.Context(), http.MethodGet, "/health", "sensitive-token", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	_, err = client.httpClient.Do(request)
	if err == nil || strings.Contains(err.Error(), "sensitive-token") {
		t.Fatalf("request error = %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("request error = %v, want socket not found", err)
	}
}

func TestNewRejectsInvalidBaseURLs(t *testing.T) {
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
