package routehttp

import (
	"bufio"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opensvc/om3/v3/core/cluster"
	"github.com/opensvc/om3/v3/core/rawconfig"
	"github.com/opensvc/om3/v3/daemon/daemonauth"
)

// newAIProxyServer serves aiProxy as the listener does, the caller
// authenticated by the strategy given.
func newAIProxyServer(t *testing.T, agentURL, strategy string) *httptest.Server {
	t.Helper()
	cluster.ConfigData.Set(&cluster.Config{ID: "cluster-1", Listener: cluster.ConfigListener{AIAgentURL: agentURL}})
	t.Cleanup(func() { cluster.ConfigData.Set(&cluster.Config{}) })
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("strategy", strategy)
			return next(c)
		}
	})
	e.Any(aiProxyPrefix+"*", aiProxy)
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return server
}

func aiErrorCode(t *testing.T, response *http.Response) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return body.Error.Code
}

func TestAIProxyRelaysToTheOm3BackendWithTheClusterID(t *testing.T) {
	var got *http.Request
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Set-Cookie", "a=b")
		_, _ = w.Write([]byte(`{"conversations":[]}`))
	}))
	defer agent.Close()
	server := newAIProxyServer(t, agent.URL, daemonauth.StrategyJWTOpenID)

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/ai/conversations?cursor=c1", nil)
	request.Header.Set("Authorization", "Bearer token-1")
	request.Header.Set("X-OpenSVC-Cluster-ID", "forged")
	request.Header.Set("Cookie", "session=s")
	request.Header.Set("Origin", "https://evil.example")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode)
	require.NotNil(t, got)
	assert.Equal(t, "/v1/om3/conversations", got.URL.Path)
	assert.Equal(t, "cursor=c1", got.URL.RawQuery)
	assert.Equal(t, "Bearer token-1", got.Header.Get("Authorization"))
	assert.Equal(t, "cluster-1", got.Header.Get("X-OpenSVC-Cluster-ID"), "the cluster id is the listener's")
	assert.Empty(t, got.Header.Get("Cookie"))
	assert.Empty(t, got.Header.Get("Origin"))
	assert.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
	assert.Empty(t, response.Header.Get("Set-Cookie"))
}

func TestAIProxyStreamsAnEventBeforeTheRunEnds(t *testing.T) {
	release := make(chan struct{})
	received := make(chan string, 1)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body) + "|" + r.Header.Get("Last-Event-Id") + "|" + r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"RUN_STARTED\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer agent.Close()
	defer close(release)
	server := newAIProxyServer(t, agent.URL, daemonauth.StrategyJWTOpenID)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/conversations/c1/runs", strings.NewReader(`{"threadId":"c1"}`))
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-Id", "7")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()

	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(response.Body).ReadString('\n')
		line <- text
	}()
	select {
	case text := <-line:
		assert.Contains(t, text, "RUN_STARTED")
	case <-time.After(5 * time.Second):
		t.Fatal("the event was held until the run ends")
	}
	// The body and the event id reach the agent, and no compression is
	// asked of it on the client's behalf.
	assert.Equal(t, `{"threadId":"c1"}|7|`, <-received)
}

func TestAIProxyTurnsAnAgentRefusalIntoTokenRejected(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"a valid OpenSVC access token is required"}}`))
	}))
	defer agent.Close()
	server := newAIProxyServer(t, agent.URL, daemonauth.StrategyJWTOpenID)

	response, err := http.Get(server.URL + "/api/ai/capabilities")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusBadGateway, response.StatusCode)
	assert.Equal(t, "ai_token_rejected", aiErrorCode(t, response))
}

// A redirect of the agent is not passed on: its location would leave the
// relay, at the root of the listener or at the origin of the agent.
func TestAIProxyRefusesTheRedirectsOfTheAgent(t *testing.T) {
	for _, location := range []string{"/v1/om3/conversations", "https://agent.internal:8090/v1/om3/conversations"} {
		agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, location, http.StatusFound)
		}))
		server := newAIProxyServer(t, agent.URL, daemonauth.StrategyJWTOpenID)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

		response, err := client.Get(server.URL + "/api/ai/conversations")
		require.NoError(t, err, location)
		assert.Equal(t, http.StatusBadGateway, response.StatusCode, location)
		assert.Empty(t, response.Header.Get("Location"), location)
		assert.Equal(t, "ai_agent_redirect", aiErrorCode(t, response), location)
		_ = response.Body.Close()
		agent.Close()
	}
}

func TestAIProxyRefusals(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the agent was called for %s", r.URL)
	}))
	defer agent.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	for name, tc := range map[string]struct {
		agentURL, strategy, method, path string
		status                           int
		code                             string
	}{
		"not configured":    {"", daemonauth.StrategyJWTOpenID, http.MethodGet, "/api/ai/capabilities", http.StatusNotFound, "ai_not_configured"},
		"not openid":        {agent.URL, daemonauth.StrategyUser, http.MethodGet, "/api/ai/capabilities", http.StatusForbidden, "ai_openid_required"},
		"daemon jwt":        {agent.URL, daemonauth.StrategyJWT, http.MethodGet, "/api/ai/capabilities", http.StatusForbidden, "ai_openid_required"},
		"dot dot":           {agent.URL, daemonauth.StrategyJWTOpenID, http.MethodGet, "/api/ai/conversations/%2e%2e/%2e%2e/health", http.StatusNotFound, "not_found"},
		"empty":             {agent.URL, daemonauth.StrategyJWTOpenID, http.MethodGet, "/api/ai/", http.StatusNotFound, "not_found"},
		"method":            {agent.URL, daemonauth.StrategyJWTOpenID, http.MethodOptions, "/api/ai/capabilities", http.StatusMethodNotAllowed, "method_not_allowed"},
		"plain http":        {"http://ai.example:8090", daemonauth.StrategyJWTOpenID, http.MethodGet, "/api/ai/capabilities", http.StatusInternalServerError, "ai_misconfigured"},
		"path in url":       {"https://ai.example:8090/v1/oc3", daemonauth.StrategyJWTOpenID, http.MethodGet, "/api/ai/capabilities", http.StatusInternalServerError, "ai_misconfigured"},
		"agent unreachable": {downURL, daemonauth.StrategyJWTOpenID, http.MethodGet, "/api/ai/capabilities", http.StatusBadGateway, "ai_agent_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			server := newAIProxyServer(t, tc.agentURL, tc.strategy)
			request, _ := http.NewRequest(tc.method, server.URL+tc.path, nil)
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, tc.status, response.StatusCode)
			assert.Equal(t, tc.code, aiErrorCode(t, response))
		})
	}
}

// An agent whose certificate the authorities of the cluster sign is relayed
// to once they are installed, ca_certificates, as the listener installs them;
// without them, the certificate is unknown and the agent unavailable.
func TestAIProxyTrustsTheAuthoritiesOfTheCluster(t *testing.T) {
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backend":"om3"}`))
	}))
	defer agent.Close()
	certs := rawconfig.Paths.Certs
	t.Cleanup(func() { rawconfig.Paths.Certs = certs })
	rawconfig.Paths.Certs = t.TempDir()
	server := newAIProxyServer(t, agent.URL, daemonauth.StrategyJWTOpenID)

	response, err := http.Get(server.URL + "/api/ai/capabilities")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, response.StatusCode)
	assert.Equal(t, "ai_agent_unavailable", aiErrorCode(t, response))
	_ = response.Body.Close()

	authority := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: agent.Certificate().Raw})
	require.NoError(t, os.WriteFile(filepath.Join(rawconfig.Paths.Certs, "ca_certificates"), authority, 0o600))
	response, err = http.Get(server.URL + "/api/ai/capabilities")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode, "the authorities installed apply at once")
}
