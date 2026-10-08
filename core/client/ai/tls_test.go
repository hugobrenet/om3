package ai

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Each server gets a unique test key, so trust isolation is tested as well.
func serveTestHTTPS(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "agent.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"agent.example.test"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "agent-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return server, caFile
}

func TestNewUsesOptionalCAFileFromEnvironment(t *testing.T) {
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing TLS or token")
		}
		_, _ = io.WriteString(w, "healthy")
	}))
	clearAgentEnv(t)
	t.Setenv(agentURLEnv, server.URL)
	t.Setenv(agentCAFileEnv, " "+caFile+" ")
	client, err := New()
	if err != nil {
		t.Fatal(err)
	}
	transport := client.httpClient.Transport.(agentOriginTransport).base.(*http.Transport)
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS12 || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("unsafe TLS configuration")
	}
	request, err := client.newAuthenticatedRequest(t.Context(), http.MethodGet, "/health", testCredential("test-token"), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "healthy" {
		t.Fatalf("response=%q err=%v", body, err)
	}
}

func TestClientRejectsUntrustedOrWrongHostnameTLS(t *testing.T) {
	var calls atomic.Int64
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	_, otherCA := serveTestHTTPS(t, http.NotFoundHandler())
	for _, tc := range []struct{ name, url, ca string }{
		{"system roots", server.URL, ""},
		{"wrong CA", server.URL, otherCA},
		{"wrong hostname", strings.Replace(server.URL, "127.0.0.1", "localhost", 1), caFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newHTTPSClient(tc.url, tc.ca)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Ask(t.Context(), testCredential("sensitive-test-token"), "health", func(Event) error { return nil })
			if err == nil || strings.Contains(err.Error(), "sensitive-test-token") {
				t.Fatalf("TLS rejection error=%v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("credentials sent before validating TLS")
	}
}

func TestNewRejectsInvalidCAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad-ca.pem")
	for _, path := range []string{"ca.pem", "/", file} {
		if _, err := newHTTPSClient("https://agent.example.test", path); err == nil {
			t.Fatalf("invalid CA path %q succeeded", path)
		}
	}
	for _, content := range []string{"not PEM", strings.Repeat("x", maxCAFileBytes+1)} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newHTTPSClient("https://agent.example.test", file); err == nil {
			t.Fatal("invalid CA accepted")
		}
	}
}

func TestHTTPSClientRejectsRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	client, err := newHTTPSClient(server.URL, caFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Ask(t.Context(), testCredential("test-token"), "health", func(Event) error { return nil })
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusTemporaryRedirect || targetCalls.Load() != 0 {
		t.Fatalf("redirect error=%v target calls=%d", err, targetCalls.Load())
	}
}

func TestHTTPSClientBindsRequestsToConfiguredOrigin(t *testing.T) {
	var calls atomic.Int64
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	client, err := newHTTPSClient(server.URL, caFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ url, host string }{
		{strings.Replace(server.URL, "https://", "http://", 1), ""},
		{"https://other.example.test", ""},
		{server.URL, "other.example.test"},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = tc.host
		request.Header.Set("Authorization", "Bearer test-token")
		if _, err := client.httpClient.Do(request); err == nil {
			t.Fatal("foreign destination accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unexpected request reached server")
	}
}

func TestHTTPSClientDisablesEnvironmentProxies(t *testing.T) {
	var proxyCalls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyCalls.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	client, err := newHTTPSClient(server.URL, caFile)
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Transport.(agentOriginTransport).base.(*http.Transport).Proxy != nil {
		t.Fatal("proxy configured")
	}
	if err := client.DeleteConversation(t.Context(), testCredential("test-token"), "conversation-1"); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("request sent to proxy")
	}
}

func TestHTTPSClientConversationsAndSSE(t *testing.T) {
	item := testConversation("conversation-1")
	var calls atomic.Int64
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.TLS == nil || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing TLS or bearer token")
		}
		if r.URL.Path == askPath || r.URL.Path == conversationTurnPath(item.ID) {
			if r.Method != http.MethodPost {
				t.Error("stream request is not POST")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set(requestIDResponseHeader, "tls-request")
			for _, event := range []Event{{Type: "text_delta", Iteration: 1, TextDelta: "healthy"}, {Type: "completed", Iteration: 1, FinishReason: "completed"}} {
				data, err := json.Marshal(event)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
				w.(http.Flusher).Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == conversationsPath:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(conversationEnvelope{Conversation: item})
		case r.Method == http.MethodGet && r.URL.Path == conversationsPath:
			_ = json.NewEncoder(w).Encode(conversationListEnvelope{Conversations: []Conversation{item}})
		default:
			http.NotFound(w, r)
		}
	}))
	client, err := newHTTPSClient(server.URL, caFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.CreateConversation(t.Context(), testCredential("test-token"))
	if err != nil || got != item {
		t.Fatalf("create=%+v err=%v", got, err)
	}
	items, err := client.ListConversations(t.Context(), testCredential("test-token"))
	if err != nil || len(items) != 1 || items[0] != item {
		t.Fatalf("list=%+v err=%v", items, err)
	}
	for _, stream := range []func(context.Context, Credential, string, EmitFunc) (string, error){
		client.Ask,
		func(ctx context.Context, cred Credential, prompt string, emit EmitFunc) (string, error) {
			return client.SendConversationTurn(ctx, cred, item.ID, prompt, emit)
		},
	} {
		var events []Event
		requestID, err := stream(t.Context(), testCredential("test-token"), "health", func(event Event) error { events = append(events, event); return nil })
		if err != nil || requestID != "tls-request" || len(events) != 2 || events[0].TextDelta != "healthy" || events[1].Type != "completed" {
			t.Fatalf("stream=%+v id=%s err=%v", events, requestID, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("requests=%d", calls.Load())
	}
}

func TestHTTPSClientPropagatesCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	server, caFile := serveTestHTTPS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
		close(canceled)
	}))
	t.Cleanup(func() { close(release) })
	client, err := newHTTPSClient(server.URL, caFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Ask(ctx, testCredential("test-token"), "health", func(Event) error { return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not cancel")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
}
