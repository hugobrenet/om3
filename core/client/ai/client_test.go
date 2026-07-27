package ai

import "testing"

func TestNewUsesDefaultLoopbackBaseURL(t *testing.T) {
	t.Setenv(baseURLEnv, "")
	client, err := New()
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if got := client.baseURL.String(); got != defaultBaseURL {
		t.Fatalf("base URL = %q, want %q", got, defaultBaseURL)
	}
}

func TestNewUsesLoopbackBaseURLFromEnvironment(t *testing.T) {
	t.Setenv(baseURLEnv, " http://127.0.0.1:19090/ ")
	client, err := New()
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if got, want := client.baseURL.String(), "http://127.0.0.1:19090"; got != want {
		t.Fatalf("base URL = %q, want %q", got, want)
	}
}

func TestNewRejectsNonLoopbackBaseURLFromEnvironment(t *testing.T) {
	t.Setenv(baseURLEnv, "https://example.com")
	if _, err := New(); err == nil {
		t.Fatal("new client accepted a non-loopback environment URL")
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
			if _, err := newClient(baseURL, nil); err == nil {
				t.Fatal("invalid base URL succeeded")
			}
		})
	}
}
