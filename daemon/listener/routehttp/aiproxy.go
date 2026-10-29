package routehttp

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/om3/v3/core/cluster"
	"github.com/opensvc/om3/v3/daemon/daemonauth"
	"github.com/opensvc/om3/v3/daemon/daemonenv"
)

const (
	// aiProxyPrefix is the path the listener relays to the om3 backend of
	// the AI agent, /v1/om3/ on the agent.
	aiProxyPrefix = "/api/ai/"

	aiAgentBackendPath = "/v1/om3/"
)

// aiProxyHeaders are the request headers relayed to the agent: the token,
// and what the body and the answer are. The cluster id is the listener's,
// never the client's.
var aiProxyHeaders = []string{"Authorization", "Content-Type", "Accept", "Last-Event-Id"}

// newAIProxyTransport reaches the agent: the authorities given, bounded
// connection setup, and no bound on a body, which streams a run.
func newAIProxyTransport(roots *x509.CertPool) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		// The bytes of the agent are relayed as they come, a run streamed: no
		// compression asked on the client's behalf.
		DisableCompression: true,
	}
}

// aiProxies keeps one reverse proxy per agent origin and state of the
// authorities of the cluster, both read at each request: a change of the
// configuration, or of the authorities installed, applies at once.
var aiProxies sync.Map

// aiAgentAuthorities tells the state of the authorities of the cluster the
// listener installs, ca_certificates, by its time and size, empty without it.
func aiAgentAuthorities() string {
	info, err := os.Stat(daemonenv.CAsCertFile())
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
}

// aiAgentRootCAs are the authorities the certificate of the agent is checked
// against: those of the system, as for an agent with a public certificate,
// and those of the cluster, the cluster ca and ca_sec_paths, as for an agent
// whose certificate a sec signed with the cluster CA, as the other clients of
// the daemon trust the nodes.
func aiAgentRootCAs() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if b, err := os.ReadFile(daemonenv.CAsCertFile()); err == nil {
		pool.AppendCertsFromPEM(b)
	}
	return pool
}

// aiProxy relays /api/ai/<path> to the om3 backend of the AI agent the
// cluster configuration names, listener.ai_agent_url, so that om3-webapp
// reaches the agent from its own origin. The authentication middleware ran
// before: the caller holds a token the daemon accepts. Only an OpenID token
// is relayed, unchanged, as the agent verifies the same token again through
// om3-mcp; the cluster id it routes by is the listener's own.
func aiProxy(c echo.Context) error {
	config := cluster.ConfigData.Get()
	if config == nil || config.Listener.AIAgentURL == "" {
		return aiProxyError(c, http.StatusNotFound, "ai_not_configured", "no AI agent is configured on this cluster: set listener.ai_agent_url in the cluster configuration")
	}
	target, err := cluster.ParseAIAgentURL(config.Listener.AIAgentURL)
	if err != nil {
		return aiProxyError(c, http.StatusInternalServerError, "ai_misconfigured", fmt.Sprintf("listener.ai_agent_url: %s", err))
	}
	if strategy, _ := c.Get("strategy").(string); strategy != daemonauth.StrategyJWTOpenID {
		return aiProxyError(c, http.StatusForbidden, "ai_openid_required", "the AI agent takes the OpenID tokens of the cluster only: sign in with the identity provider")
	}
	rest, ok := aiProxyPath(c.Param("*"))
	if !ok {
		return aiProxyError(c, http.StatusNotFound, "not_found", "no such AI agent route")
	}
	switch c.Request().Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return aiProxyError(c, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	request := c.Request().Clone(c.Request().Context())
	request.URL.Path = aiAgentBackendPath + rest
	request.URL.RawPath = ""
	header := http.Header{}
	for _, name := range aiProxyHeaders {
		if values := c.Request().Header.Values(name); len(values) > 0 {
			header[http.CanonicalHeaderKey(name)] = values
		}
	}
	header.Set("X-OpenSVC-Cluster-ID", config.ID)
	request.Header = header
	aiReverseProxy(target).ServeHTTP(c.Response(), request)
	return nil
}

// aiProxyPath gives the path under /v1/om3/ a client asked, decoded as the
// agent will read it, refused when it would leave it, as a .. segment, even
// encoded, or is empty.
func aiProxyPath(raw string) (string, bool) {
	rest, err := url.PathUnescape(raw)
	if err != nil || rest == "" || strings.HasPrefix(rest, "/") || strings.Contains(rest, "\\") {
		return "", false
	}
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	if path.Clean(rest) != rest {
		return "", false
	}
	return rest, true
}

func aiReverseProxy(target *url.URL) *httputil.ReverseProxy {
	key := target.String() + " " + aiAgentAuthorities()
	if proxy, ok := aiProxies.Load(key); ok {
		return proxy.(*httputil.ReverseProxy)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		Transport: newAIProxyTransport(aiAgentRootCAs()),
		// A run streams its events: each is written as it comes.
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")
			for name := range response.Header {
				if strings.HasPrefix(name, "Access-Control-") {
					response.Header.Del(name)
				}
			}
			if response.StatusCode == http.StatusUnauthorized {
				// The daemon accepted the token: the agent verifies it
				// against the issuer and the audience of the om3-mcp
				// catalogue, which differ. A 401 would send the webapp
				// to sign in again, in a loop.
				return replaceAIResponse(response, "ai_token_rejected", "the daemon accepts this token, the AI agent refuses it: check the OpenID issuer and audience of this cluster in the om3-mcp catalogue")
			}
			if response.StatusCode >= 300 && response.StatusCode < 400 && response.StatusCode != http.StatusNotModified {
				// The agent serves no redirect: followed, its location
				// would leave the relay, at the root of the listener or
				// at the origin of the agent.
				return replaceAIResponse(response, "ai_agent_redirect", fmt.Sprintf("the AI agent answered %d, a redirect the relay does not follow", response.StatusCode))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				// The client left, as a stop of a run: nothing to answer.
				return
			}
			writeAIProxyError(w, http.StatusBadGateway, "ai_agent_unavailable", fmt.Sprintf("the AI agent %s did not answer: %s", target.Host, err))
		},
	}
	actual, _ := aiProxies.LoadOrStore(key, proxy)
	return actual.(*httputil.ReverseProxy)
}

// replaceAIResponse replaces an answer of the agent the relay does not pass
// on, its headers and body dropped, by a 502 with an error of its own.
func replaceAIResponse(response *http.Response, code, message string) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	body, err := json.Marshal(aiProxyErrorBody(code, message))
	if err != nil {
		return err
	}
	response.StatusCode = http.StatusBadGateway
	response.Status = fmt.Sprintf("%d %s", http.StatusBadGateway, http.StatusText(http.StatusBadGateway))
	response.Header = http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(body))}}
	response.ContentLength = int64(len(body))
	response.Body = io.NopCloser(strings.NewReader(string(body)))
	return nil
}

// aiProxyErrorBody is an error in the shape the agent answers its own, which
// om3-webapp reads.
func aiProxyErrorBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}

func aiProxyError(c echo.Context, status int, code, message string) error {
	return c.JSON(status, aiProxyErrorBody(code, message))
}

func writeAIProxyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(aiProxyErrorBody(code, message))
}
