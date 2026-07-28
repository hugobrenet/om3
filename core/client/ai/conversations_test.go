package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientListsConversations(t *testing.T) {
	first := testConversation("conversation-1")
	second := testConversation("conversation-2")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != conversationsPath {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set(requestIDResponseHeader, "request-list")
		_ = json.NewEncoder(response).Encode(conversationListEnvelope{Conversations: []Conversation{first, second}})
	}))
	t.Cleanup(server.Close)

	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	items, err := client.ListConversations(t.Context(), "token")
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(items) != 2 || items[0] != first || items[1] != second {
		t.Fatalf("conversations = %#v", items)
	}
}

func TestClientCreatesConversation(t *testing.T) {
	item := testConversation("conversation-1")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != conversationsPath {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if request.ContentLength != 0 {
			t.Errorf("Content-Length = %d", request.ContentLength)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(conversationEnvelope{Conversation: item})
	}))
	t.Cleanup(server.Close)

	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	got, err := client.CreateConversation(t.Context(), "token")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if got != item {
		t.Fatalf("conversation = %#v, want %#v", got, item)
	}
}

func TestClientSendsConversationTurn(t *testing.T) {
	const id = "conversation-1"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != conversationTurnPath(id) {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer turn-token" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Prompt != "continue assessment" {
			t.Errorf("prompt = %q", body.Prompt)
		}
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set(requestIDResponseHeader, "request-turn")
		_, _ = fmt.Fprint(response, "event: text_delta\ndata: {\"type\":\"text_delta\",\"iteration\":1,\"text_delta\":\"continuing\"}\n\n")
		_, _ = fmt.Fprint(response, "event: completed\ndata: {\"type\":\"completed\",\"iteration\":1,\"finish_reason\":\"completed\"}\n\n")
	}))
	t.Cleanup(server.Close)

	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var events []Event
	requestID, err := client.SendConversationTurn(t.Context(), "turn-token", id, "continue assessment", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("send conversation turn: %v", err)
	}
	if requestID != "request-turn" || len(events) != 2 || events[0].TextDelta != "continuing" || events[1].Type != "completed" {
		t.Fatalf("request ID = %q, events = %#v", requestID, events)
	}
}

func TestClientUpdatesConversationTitle(t *testing.T) {
	const id = "conversation-1"
	item := testConversation(id)
	item.Title = "Renamed conversation"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != conversationPath(id) {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body conversationTitleRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Title != item.Title {
			t.Errorf("title = %q", body.Title)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(conversationEnvelope{Conversation: item})
	}))
	t.Cleanup(server.Close)

	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	got, err := client.UpdateConversationTitle(t.Context(), "token", id, "  Renamed\nconversation  ")
	if err != nil {
		t.Fatalf("update conversation title: %v", err)
	}
	if got != item {
		t.Fatalf("conversation = %#v, want %#v", got, item)
	}
}

func TestClientGetsAndDeletesConversation(t *testing.T) {
	const id = "conversation-1"
	item := testConversation(id)
	var deleteCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != conversationPath(id) {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		switch request.Method {
		case http.MethodGet:
			if got := request.Header.Get("Accept"); got != "application/json" {
				t.Errorf("Accept = %q", got)
			}
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(conversationEnvelope{Conversation: item})
		case http.MethodDelete:
			if got := request.Header.Get("Accept"); got != "application/json" {
				t.Errorf("Accept = %q", got)
			}
			deleteCalls.Add(1)
			response.WriteHeader(http.StatusNoContent)
		default:
			response.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	got, err := client.GetConversation(t.Context(), "token", id)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if got != item {
		t.Fatalf("conversation = %#v, want %#v", got, item)
	}
	if err := client.DeleteConversation(t.Context(), "token", id); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}
	if deleteCalls.Load() != 1 {
		t.Fatalf("delete calls = %d", deleteCalls.Load())
	}
}

func TestConversationMethodsRejectInvalidInputBeforeRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(server.Close)
	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := client.ListConversations(t.Context(), " "); err == nil {
		t.Fatal("list accepted an empty token")
	}
	if _, err := client.CreateConversation(t.Context(), " "); err == nil {
		t.Fatal("create accepted an empty token")
	}
	for _, title := range []string{" \n ", strings.Repeat("a", maxConversationTitleRunes+1)} {
		if _, err := client.UpdateConversationTitle(t.Context(), "token", "conversation-1", title); err == nil {
			t.Errorf("update accepted title %q", title)
		}
	}
	for _, id := range []string{"", "bad/id", "bad id", strings.Repeat("a", maxConversationIDBytes+1)} {
		if _, err := client.GetConversation(t.Context(), "token", id); err == nil {
			t.Errorf("get accepted ID %q", id)
		}
		if err := client.DeleteConversation(t.Context(), "token", id); err == nil {
			t.Errorf("delete accepted ID %q", id)
		}
		if _, err := client.SendConversationTurn(t.Context(), "token", id, "prompt", func(Event) error { return nil }); err == nil {
			t.Errorf("turn accepted ID %q", id)
		}
		if _, err := client.UpdateConversationTitle(t.Context(), "token", id, "valid"); err == nil {
			t.Errorf("update accepted ID %q", id)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d", calls.Load())
	}
}

func TestConversationMethodsReturnSanitizedAPIErrors(t *testing.T) {
	const token = "sensitive-token-marker"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(requestIDResponseHeader, "request-error")
		response.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(response, `{"error":{"code":"conversation_not_found","message":"bad %s"}}`, token)
	}))
	t.Cleanup(server.Close)
	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.GetConversation(t.Context(), token, "conversation-1")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusNotFound || apiError.Code != "conversation_not_found" || apiError.RequestID != "request-error" {
		t.Fatalf("API error = %#v, %v", apiError, err)
	}
	if strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error = %q", err)
	}
	if err := client.DeleteConversation(t.Context(), token, "conversation-1"); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("delete error = %v", err)
	}
	if _, err := client.UpdateConversationTitle(t.Context(), token, "conversation-1", "valid"); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("update error = %v", err)
	}
}

func TestClientRejectsInvalidConversationResponses(t *testing.T) {
	valid := testConversation("conversation-1")
	invalidTitle := valid
	invalidTitle.Title = " not normalized "
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "content type", contentType: "text/plain", body: `{"conversations":[]}`},
		{name: "malformed JSON", contentType: "application/json", body: `{"conversations":`},
		{name: "multiple JSON values", contentType: "application/json", body: `{"conversations":[]} {}`},
		{name: "invalid metadata", contentType: "application/json", body: `{"conversations":[{"id":"conversation-1","stored_bytes":0}]}`},
		{name: "invalid title", contentType: "application/json", body: mustJSON(t, conversationListEnvelope{Conversations: []Conversation{invalidTitle}})},
		{name: "duplicate IDs", contentType: "application/json", body: mustJSON(t, conversationListEnvelope{Conversations: []Conversation{valid, valid}})},
		{name: "oversized", contentType: "application/json", body: `{"conversations":[]}` + strings.Repeat(" ", maxConversationResponseBodyBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", test.contentType)
				response.Header().Set(requestIDResponseHeader, "request-invalid")
				_, _ = fmt.Fprint(response, test.body)
			}))
			t.Cleanup(server.Close)
			client, err := newClient(server.URL, server.Client())
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			if _, err := client.ListConversations(t.Context(), "token"); err == nil {
				t.Fatal("invalid response succeeded")
			}
		})
	}
}

func TestClientRejectsMismatchedConversationID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(conversationEnvelope{Conversation: testConversation("conversation-2")})
	}))
	t.Cleanup(server.Close)
	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.GetConversation(t.Context(), "token", "conversation-1"); err == nil {
		t.Fatal("mismatched conversation ID succeeded")
	}
}

func TestClientRejectsMismatchedConversationTitle(t *testing.T) {
	item := testConversation("conversation-1")
	item.Title = "Different title"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(conversationEnvelope{Conversation: item})
	}))
	t.Cleanup(server.Close)
	client, err := newClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.UpdateConversationTitle(t.Context(), "token", item.ID, "Expected title"); err == nil {
		t.Fatal("mismatched conversation title succeeded")
	}
}

func testConversation(id string) Conversation {
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	return Conversation{
		ID: id, Title: "Cluster health", CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Minute),
		ExpiresAt: createdAt.Add(7 * 24 * time.Hour), StoredBytes: 1024,
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(data)
}
