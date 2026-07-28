package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	conversationsPath                = "/v1/conversations"
	maxConversationIDBytes           = 128
	maxConversationTitleRunes        = 80
	maxConversationListItems         = 100
	maxConversationResponseBodyBytes = 1 << 20
)

type Conversation struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	StoredBytes int64     `json:"stored_bytes"`
}

type conversationEnvelope struct {
	Conversation Conversation `json:"conversation"`
}

type conversationListEnvelope struct {
	Conversations []Conversation `json:"conversations"`
}

type conversationTitleRequest struct {
	Title string `json:"title"`
}

func (c *Client) CreateConversation(ctx context.Context, token string) (Conversation, error) {
	var payload conversationEnvelope
	if err := c.doJSON(ctx, http.MethodPost, conversationsPath, token, http.StatusCreated, &payload); err != nil {
		return Conversation{}, err
	}
	if err := validateConversation(payload.Conversation); err != nil {
		return Conversation{}, fmt.Errorf("validate ai agent conversation: %w", err)
	}
	return payload.Conversation, nil
}

func (c *Client) ListConversations(ctx context.Context, token string) ([]Conversation, error) {
	var payload conversationListEnvelope
	if err := c.doJSON(ctx, http.MethodGet, conversationsPath, token, http.StatusOK, &payload); err != nil {
		return nil, err
	}
	if len(payload.Conversations) > maxConversationListItems {
		return nil, fmt.Errorf("ai agent returned more than %d conversations", maxConversationListItems)
	}
	seen := make(map[string]struct{}, len(payload.Conversations))
	for index, conversation := range payload.Conversations {
		if err := validateConversation(conversation); err != nil {
			return nil, fmt.Errorf("validate ai agent conversation %d: %w", index, err)
		}
		if _, ok := seen[conversation.ID]; ok {
			return nil, fmt.Errorf("ai agent returned duplicate conversation ID")
		}
		seen[conversation.ID] = struct{}{}
	}
	if payload.Conversations == nil {
		payload.Conversations = make([]Conversation, 0)
	}
	return payload.Conversations, nil
}

func (c *Client) GetConversation(ctx context.Context, token string, id string) (Conversation, error) {
	if err := validateConversationID(id); err != nil {
		return Conversation{}, err
	}
	var payload conversationEnvelope
	if err := c.doJSON(ctx, http.MethodGet, conversationPath(id), token, http.StatusOK, &payload); err != nil {
		return Conversation{}, err
	}
	if err := validateConversation(payload.Conversation); err != nil {
		return Conversation{}, fmt.Errorf("validate ai agent conversation: %w", err)
	}
	if payload.Conversation.ID != id {
		return Conversation{}, fmt.Errorf("ai agent returned a different conversation ID")
	}
	return payload.Conversation, nil
}

func (c *Client) DeleteConversation(ctx context.Context, token string, id string) error {
	if err := validateConversationID(id); err != nil {
		return err
	}
	request, err := c.newAuthenticatedRequest(ctx, http.MethodDelete, conversationPath(id), token, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send ai agent request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return decodeAPIError(response, response.Header.Get(requestIDResponseHeader), token)
	}
	return nil
}

func (c *Client) UpdateConversationTitle(ctx context.Context, token string, id string, title string) (Conversation, error) {
	if err := validateConversationID(id); err != nil {
		return Conversation{}, err
	}
	title, err := normalizeConversationTitle(title)
	if err != nil {
		return Conversation{}, err
	}
	body, err := json.Marshal(conversationTitleRequest{Title: title})
	if err != nil {
		return Conversation{}, fmt.Errorf("encode ai agent conversation title request: %w", err)
	}
	var payload conversationEnvelope
	if err := c.doJSONRequest(ctx, http.MethodPatch, conversationPath(id), token, http.StatusOK, bytes.NewReader(body), &payload); err != nil {
		return Conversation{}, err
	}
	if err := validateConversation(payload.Conversation); err != nil {
		return Conversation{}, fmt.Errorf("validate ai agent conversation: %w", err)
	}
	if payload.Conversation.ID != id {
		return Conversation{}, fmt.Errorf("ai agent returned a different conversation ID")
	}
	if payload.Conversation.Title != title {
		return Conversation{}, fmt.Errorf("ai agent returned a different conversation title")
	}
	return payload.Conversation, nil
}

func (c *Client) SendConversationTurn(ctx context.Context, token string, id string, prompt string, emit EmitFunc) (string, error) {
	if err := validateConversationID(id); err != nil {
		return "", err
	}
	return c.streamPrompt(ctx, conversationTurnPath(id), token, prompt, emit)
}

func (c *Client) doJSON(ctx context.Context, method string, path string, token string, expectedStatus int, target any) error {
	return c.doJSONRequest(ctx, method, path, token, expectedStatus, nil, target)
}

func (c *Client) doJSONRequest(ctx context.Context, method string, path string, token string, expectedStatus int, requestBody io.Reader, target any) error {
	request, err := c.newAuthenticatedRequest(ctx, method, path, token, requestBody)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send ai agent request: %w", err)
	}
	defer response.Body.Close()
	requestID := response.Header.Get(requestIDResponseHeader)
	if response.StatusCode != expectedStatus {
		return decodeAPIError(response, requestID, token)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("ai agent response Content-Type is not application/json (request_id=%s)", requestID)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxConversationResponseBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read ai agent JSON response (request_id=%s): %w", requestID, err)
	}
	if len(responseBody) > maxConversationResponseBodyBytes {
		return fmt.Errorf("ai agent JSON response exceeds %d bytes (request_id=%s)", maxConversationResponseBodyBytes, requestID)
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode ai agent JSON response (request_id=%s): %w", requestID, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode ai agent JSON response (request_id=%s): multiple JSON values", requestID)
		}
		return fmt.Errorf("decode ai agent JSON response (request_id=%s): %w", requestID, err)
	}
	return nil
}

func conversationPath(id string) string {
	return conversationsPath + "/" + id
}

func conversationTurnPath(id string) string {
	return conversationPath(id) + "/turns"
}

func validateConversation(conversation Conversation) error {
	if err := validateConversationID(conversation.ID); err != nil {
		return err
	}
	if conversation.CreatedAt.IsZero() || conversation.UpdatedAt.IsZero() || conversation.ExpiresAt.IsZero() {
		return fmt.Errorf("conversation timestamps are incomplete")
	}
	if conversation.StoredBytes < 0 {
		return fmt.Errorf("conversation stored byte count is negative")
	}
	if conversation.Title != "" {
		title, err := normalizeConversationTitle(conversation.Title)
		if err != nil || title != conversation.Title {
			return fmt.Errorf("conversation title is invalid")
		}
	}
	return nil
}

func normalizeConversationTitle(title string) (string, error) {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return "", fmt.Errorf("ai agent conversation title is empty")
	}
	if utf8.RuneCountInString(title) > maxConversationTitleRunes {
		return "", fmt.Errorf("ai agent conversation title exceeds %d characters", maxConversationTitleRunes)
	}
	return title, nil
}

func validateConversationID(id string) error {
	if id == "" || len(id) > maxConversationIDBytes {
		return fmt.Errorf("ai agent conversation ID must contain between 1 and %d bytes", maxConversationIDBytes)
	}
	for index := 0; index < len(id); index++ {
		value := id[index]
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') {
			continue
		}
		switch value {
		case '-', '.', '_', '~':
			continue
		default:
			return fmt.Errorf("ai agent conversation ID contains an invalid character")
		}
	}
	return nil
}
