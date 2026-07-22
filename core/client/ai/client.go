package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

const (
	DefaultEndpoint         = "http://127.0.0.1:8090/v1/ask"
	maxPromptBytes          = 32 << 10
	maxErrorBodyBytes       = 64 << 10
	maxStreamBytes          = 16 << 20
	maxStreamLineBytes      = 1 << 20
	maxStreamEventBytes     = 2 << 20
	maxErrorCodeRunes       = 128
	maxErrorMessageRunes    = 2048
	requestIDResponseHeader = "X-Request-ID"
)

type Client struct {
	endpoint   string
	httpClient *http.Client
}

type Event struct {
	Type         string `json:"type"`
	Iteration    int    `json:"iteration,omitempty"`
	TextDelta    string `json:"text_delta,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	ToolError    *bool  `json:"tool_error,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type EmitFunc func(Event) error

type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("ai agent returned HTTP %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.Code != "" {
		message += ": " + e.Code
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	if e.RequestID != "" {
		message += " (request_id=" + e.RequestID + ")"
	}
	return message
}

type StreamError struct {
	Code      string
	Message   string
	RequestID string
}

func (e *StreamError) Error() string {
	message := "ai agent stream failed"
	if e.Code != "" {
		message += ": " + e.Code
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	if e.RequestID != "" {
		message += " (request_id=" + e.RequestID + ")"
	}
	return message
}

func New(endpoint string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse ai agent endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("ai agent endpoint scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("ai agent endpoint host is empty")
	}
	if parsed.Path != "/v1/ask" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("ai agent endpoint must end with the exact /v1/ask path and contain no credentials, query, or fragment")
	}
	if parsed.Scheme == "http" {
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("plain HTTP ai agent endpoint must use a loopback IP")
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{endpoint: parsed.String(), httpClient: httpClient}, nil
}

func (c *Client) Ask(ctx context.Context, token string, prompt string, emit EmitFunc) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("ai agent Bearer token is empty")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("ai agent prompt is empty")
	}
	if len(prompt) > maxPromptBytes {
		return "", fmt.Errorf("ai agent prompt exceeds %d bytes", maxPromptBytes)
	}
	if emit == nil {
		return "", fmt.Errorf("ai agent event consumer is nil")
	}
	body, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("encode ai agent request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create ai agent request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("send ai agent request: %w", err)
	}
	defer response.Body.Close()
	requestID := response.Header.Get(requestIDResponseHeader)
	if response.StatusCode != http.StatusOK {
		return requestID, decodeAPIError(response, requestID, token)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return requestID, fmt.Errorf("ai agent response Content-Type is not text/event-stream (request_id=%s)", requestID)
	}
	if err := consumeStream(response.Body, requestID, token, emit); err != nil {
		return requestID, err
	}
	return requestID, nil
}

func decodeAPIError(response *http.Response, requestID string, token string) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	if err != nil || len(body) > maxErrorBodyBytes {
		return &APIError{StatusCode: response.StatusCode, RequestID: requestID}
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return &APIError{StatusCode: response.StatusCode, RequestID: requestID}
	}
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       redactSecret(normalizeErrorText(payload.Error.Code, maxErrorCodeRunes), token),
		Message:    redactSecret(normalizeErrorText(payload.Error.Message, maxErrorMessageRunes), token),
		RequestID:  requestID,
	}
}

func consumeStream(reader io.Reader, requestID string, token string, emit EmitFunc) error {
	limited := &io.LimitedReader{R: reader, N: maxStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxStreamLineBytes)
	var (
		data      bytes.Buffer
		eventName string
		terminal  bool
	)
	dispatch := func() error {
		if data.Len() == 0 {
			eventName = ""
			return nil
		}
		if terminal {
			return fmt.Errorf("ai agent stream contains an event after its terminal event (request_id=%s)", requestID)
		}
		var event Event
		if err := json.Unmarshal(data.Bytes(), &event); err != nil {
			return fmt.Errorf("decode ai agent SSE event: %w", err)
		}
		data.Reset()
		if event.Type == "" || (eventName != "" && event.Type != eventName) {
			return fmt.Errorf("ai agent SSE event type is invalid (request_id=%s)", requestID)
		}
		if err := validateEvent(event); err != nil {
			return fmt.Errorf("validate ai agent SSE event: %w", err)
		}
		if event.Type == "error" {
			event.Code = redactSecret(normalizeErrorText(event.Code, maxErrorCodeRunes), token)
			event.Message = redactSecret(normalizeErrorText(event.Message, maxErrorMessageRunes), token)
		}
		if err := emit(event); err != nil {
			return fmt.Errorf("consume ai agent event: %w", err)
		}
		switch event.Type {
		case "completed":
			terminal = true
		case "error":
			terminal = true
			return &StreamError{
				Code:      event.Code,
				Message:   event.Message,
				RequestID: requestID,
			}
		}
		eventName = ""
		return nil
	}

	for scanner.Scan() {
		line := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		if len(line) == 0 {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte{':'})
		if !found {
			continue
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(field) {
		case "event":
			eventName = string(value)
		case "data":
			if data.Len() != 0 {
				data.WriteByte('\n')
			}
			if data.Len()+len(value) > maxStreamEventBytes {
				return fmt.Errorf("ai agent SSE event exceeds %d bytes", maxStreamEventBytes)
			}
			data.Write(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan ai agent SSE stream: %w", err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("ai agent SSE stream exceeds %d bytes", maxStreamBytes)
	}
	if err := dispatch(); err != nil {
		return err
	}
	if !terminal {
		return fmt.Errorf("ai agent SSE stream ended without a terminal event (request_id=%s)", requestID)
	}
	return nil
}

func validateEvent(event Event) error {
	if event.Iteration < 0 {
		return fmt.Errorf("iteration is negative")
	}
	switch event.Type {
	case "text_delta":
		if event.Iteration <= 0 || event.TextDelta == "" {
			return fmt.Errorf("text_delta event is incomplete")
		}
	case "tool_started":
		if event.Iteration <= 0 || event.ToolName == "" || event.ToolError != nil {
			return fmt.Errorf("tool_started event is incomplete")
		}
	case "tool_finished":
		if event.Iteration <= 0 || event.ToolName == "" || event.ToolError == nil {
			return fmt.Errorf("tool_finished event is incomplete")
		}
	case "usage":
		if event.Iteration <= 0 || event.Usage == nil || event.Usage.InputTokens < 0 || event.Usage.OutputTokens < 0 || event.Usage.TotalTokens < 0 {
			return fmt.Errorf("usage event is incomplete")
		}
	case "completed":
		if event.Iteration <= 0 || event.FinishReason == "" {
			return fmt.Errorf("completed event is incomplete")
		}
	case "error":
		if event.Code == "" || event.Message == "" {
			return fmt.Errorf("error event is incomplete")
		}
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
	return nil
}

func normalizeErrorText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func redactSecret(value string, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[redacted]")
}
