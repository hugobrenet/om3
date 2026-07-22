package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientAskStreamsEvents(t *testing.T) {
	const token = "test-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/ask" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q", got)
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Prompt != "health of my cluster" {
			t.Errorf("prompt = %q", body.Prompt)
		}
		response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		response.Header().Set(requestIDResponseHeader, "request-1")
		_, _ = fmt.Fprint(response, "event: text_delta\ndata: {\"type\":\"text_delta\",\"iteration\":1,\"text_delta\":\"checking\"}\n\n")
		_, _ = fmt.Fprint(response, "event: tool_started\ndata: {\"type\":\"tool_started\",\"iteration\":1,\"tool_name\":\"get_cluster_health\"}\n\n")
		_, _ = fmt.Fprint(response, "event: tool_finished\ndata: {\"type\":\"tool_finished\",\"iteration\":1,\"tool_name\":\"get_cluster_health\",\"tool_error\":false}\n\n")
		_, _ = fmt.Fprint(response, "event: usage\ndata: {\"type\":\"usage\",\"iteration\":1,\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(response, "event: completed\ndata: {\"type\":\"completed\",\"iteration\":2,\"finish_reason\":\"completed\"}\n\n")
	}))
	t.Cleanup(server.Close)

	client, err := New(server.URL+"/v1/ask", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var events []Event
	requestID, err := client.Ask(t.Context(), token, "health of my cluster", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if requestID != "request-1" {
		t.Fatalf("request ID = %q", requestID)
	}
	if len(events) != 5 || events[0].TextDelta != "checking" || events[4].Type != "completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientAskReturnsBoundedAPIErrorWithoutToken(t *testing.T) {
	const token = "sensitive-token-marker"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(requestIDResponseHeader, "request-2")
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(response, `{"error":{"code":"unauthorized","message":"bad %s\ndetail"}}`, token)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/v1/ask", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	requestID, err := client.Ask(t.Context(), token, "health", func(Event) error { return nil })
	if err == nil {
		t.Fatal("ask succeeded")
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusUnauthorized || apiError.RequestID != "request-2" {
		t.Fatalf("api error = %#v, %v", apiError, err)
	}
	if requestID != "request-2" || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error = %q, request ID = %q", err, requestID)
	}
}

func TestClientAskReturnsSanitizedStreamError(t *testing.T) {
	const token = "sensitive-token-marker"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set(requestIDResponseHeader, "request-3")
		_, _ = fmt.Fprintf(response, "event: error\ndata: {\"type\":\"error\",\"code\":\"agent_failed\",\"message\":\"bad %s\"}\n\n", token)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/v1/ask", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var emitted Event
	_, err = client.Ask(t.Context(), token, "health", func(event Event) error {
		emitted = event
		return nil
	})
	var streamError *StreamError
	if !errors.As(err, &streamError) || streamError.Code != "agent_failed" {
		t.Fatalf("stream error = %#v, %v", streamError, err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(emitted.Message, token) {
		t.Fatal("stream error exposes token")
	}
}

func TestClientRejectsRedirectAndInvalidEndpoints(t *testing.T) {
	var targetCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			targetCalls.Add(1)
			return
		}
		http.Redirect(response, request, "/target", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/v1/ask", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Ask(t.Context(), "token", "health", func(Event) error { return nil })
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusTemporaryRedirect || targetCalls.Load() != 0 {
		t.Fatalf("redirect error = %v, target calls = %d", err, targetCalls.Load())
	}

	for _, endpoint := range []string{
		"http://example.com/v1/ask",
		"ftp://127.0.0.1/v1/ask",
		"http://127.0.0.1/v1/ask/",
		"http://user:pass@127.0.0.1/v1/ask",
		"http://127.0.0.1/v1/ask?token=value",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := New(endpoint, nil); err == nil {
				t.Fatal("invalid endpoint succeeded")
			}
		})
	}
}

func TestClientRejectsMalformedOrIncompleteStream(t *testing.T) {
	for _, body := range []string{
		"event: text_delta\ndata: {\"type\":\"text_delta\",\"iteration\":1,\"text_delta\":\"partial\"}\n\n",
		"event: completed\ndata: {\"type\":\"completed\",\"iteration\":1}\n\n",
		"event: text_delta\ndata: not-json\n\n",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(response, body)
		}))
		client, err := New(server.URL+"/v1/ask", server.Client())
		if err != nil {
			server.Close()
			t.Fatalf("new client: %v", err)
		}
		_, err = client.Ask(context.Background(), "token", "health", func(Event) error { return nil })
		server.Close()
		if err == nil {
			t.Fatalf("stream %q succeeded", body)
		}
	}
}
