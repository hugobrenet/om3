package cluster

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseAIAgentURL reads listener.ai_agent_url: an https origin, or http to a
// loopback address, without credentials, path, query or fragment.
func ParseAIAgentURL(value string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	switch {
	case u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("%q is no origin: give the scheme, the host and the port only", value)
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
	default:
		return nil, fmt.Errorf("%q: https is required, http only to a loopback address", value)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
