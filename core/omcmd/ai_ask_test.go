package omcmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
	"github.com/opensvc/om3/v3/daemon/api"
)

type fakeAuthTokenClient struct {
	token    string
	status   int
	duration string
	err      error
}

func (c *fakeAuthTokenClient) PostAuthTokenWithResponse(_ context.Context, params *api.PostAuthTokenParams, _ ...api.RequestEditorFn) (*api.PostAuthTokenResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	if params.AccessDuration != nil {
		c.duration = *params.AccessDuration
	}
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	response := &api.PostAuthTokenResponse{
		HTTPResponse: &http.Response{StatusCode: status},
	}
	if status == http.StatusOK {
		response.JSON200 = &api.AuthToken{AccessToken: c.token}
	}
	return response, nil
}

type fakeAIAgentClient struct {
	token       string
	prompt      string
	hasDeadline bool
	err         error
}

func (c *fakeAIAgentClient) Ask(ctx context.Context, token string, prompt string, emit clientai.EmitFunc) (string, error) {
	c.token = token
	c.prompt = prompt
	_, c.hasDeadline = ctx.Deadline()
	if c.err != nil {
		return "request-1", c.err
	}
	toolError := false
	for _, event := range []clientai.Event{
		{Type: "tool_started", Iteration: 1, ToolName: "get_cluster_health"},
		{Type: "tool_finished", Iteration: 1, ToolName: "get_cluster_health", ToolError: &toolError},
		{Type: "text_delta", Iteration: 2, TextDelta: "cluster "},
		{Type: "text_delta", Iteration: 2, TextDelta: "healthy"},
		{Type: "completed", Iteration: 2, FinishReason: "completed"},
	} {
		if err := emit(event); err != nil {
			return "request-1", err
		}
	}
	return "request-1", nil
}

func TestCmdAIAskGetsTokenAndStreamsAgentResponse(t *testing.T) {
	const token = "sensitive-token-marker"
	tokenClient := &fakeAuthTokenClient{token: token}
	agentClient := &fakeAIAgentClient{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := &CmdAIAsk{
		Prompt:  "health of my cluster",
		Timeout: 2 * time.Second,
		Out:     &stdout,
		ErrOut:  &stderr,
		newAuthTokenClient: func() (authTokenClient, error) {
			return tokenClient, nil
		},
		newAIAgentClient: func() (aiAgentClient, error) {
			return agentClient, nil
		},
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if tokenClient.duration != (time.Minute + 2*time.Second).String() {
		t.Fatalf("token duration = %q", tokenClient.duration)
	}
	if agentClient.token != token || agentClient.prompt != command.Prompt || !agentClient.hasDeadline {
		t.Fatalf("agent call token=%q prompt=%q deadline=%v", agentClient.token, agentClient.prompt, agentClient.hasDeadline)
	}
	if stdout.String() != "cluster healthy\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.String() != "[tool] get_cluster_health\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), token) {
		t.Fatal("command output exposes token")
	}
}

func TestCmdAIAskPropagatesSafeFailures(t *testing.T) {
	target := errors.New("agent unavailable")
	for _, test := range []struct {
		name       string
		command    CmdAIAsk
		wantTarget error
		wantText   string
	}{
		{name: "empty prompt", command: CmdAIAsk{Timeout: time.Second}, wantText: "prompt is empty"},
		{name: "short timeout", command: CmdAIAsk{Prompt: "health", Timeout: time.Millisecond}, wantText: "timeout must be"},
		{
			name: "token client",
			command: CmdAIAsk{
				Prompt:  "health",
				Timeout: time.Second,
				newAuthTokenClient: func() (authTokenClient, error) {
					return nil, target
				},
			},
			wantTarget: target,
		},
		{
			name: "agent",
			command: CmdAIAsk{
				Prompt:  "health",
				Timeout: time.Second,
				newAuthTokenClient: func() (authTokenClient, error) {
					return &fakeAuthTokenClient{token: "token"}, nil
				},
				newAIAgentClient: func() (aiAgentClient, error) {
					return &fakeAIAgentClient{err: target}, nil
				},
			},
			wantTarget: target,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.command.Run(t.Context())
			if err == nil {
				t.Fatal("command succeeded")
			}
			if test.wantTarget != nil && !errors.Is(err, test.wantTarget) {
				t.Fatalf("error = %v, want wrapping %v", err, test.wantTarget)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want containing %q", err, test.wantText)
			}
		})
	}
}
