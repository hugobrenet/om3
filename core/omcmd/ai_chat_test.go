package omcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
)

type fakeAIChatClient struct {
	conversation clientai.Conversation
	createToken  string
	getToken     string
	getID        string
	turnTokens   []string
	turnPrompts  []string
	turn         func(context.Context, clientai.EmitFunc) error
	err          error
}

func (c *fakeAIChatClient) CreateConversation(_ context.Context, token string) (clientai.Conversation, error) {
	c.createToken = token
	if c.err != nil {
		return clientai.Conversation{}, c.err
	}
	return c.conversation, nil
}

func (c *fakeAIChatClient) GetConversation(_ context.Context, token string, id string) (clientai.Conversation, error) {
	c.getToken = token
	c.getID = id
	if c.err != nil {
		return clientai.Conversation{}, c.err
	}
	return c.conversation, nil
}

func (c *fakeAIChatClient) SendConversationTurn(ctx context.Context, token string, _ string, prompt string, emit clientai.EmitFunc) (string, error) {
	c.turnTokens = append(c.turnTokens, token)
	c.turnPrompts = append(c.turnPrompts, prompt)
	if c.turn != nil {
		return "request-turn", c.turn(ctx, emit)
	}
	if c.err != nil {
		return "request-turn", c.err
	}
	toolError := false
	for _, event := range []clientai.Event{
		{Type: "tool_started", Iteration: 1, ToolName: "get_cluster_health"},
		{Type: "tool_finished", Iteration: 1, ToolName: "get_cluster_health", ToolError: &toolError},
		{Type: "text_delta", Iteration: 2, TextDelta: "answer: " + prompt},
		{Type: "completed", Iteration: 2, FinishReason: "completed"},
	} {
		if err := emit(event); err != nil {
			return "request-turn", err
		}
	}
	return "request-turn", nil
}

func TestCmdAIChatCreatesConversationAndRunsTurns(t *testing.T) {
	conversation := chatTestConversation("conversation-1")
	client := &fakeAIChatClient{conversation: conversation}
	tokenCalls := 0
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := &CmdAIChat{
		Timeout: 2 * time.Second,
		In:      strings.NewReader("first prompt\nsecond prompt\nexit\n"),
		Out:     &stdout,
		ErrOut:  &stderr,
		newAuthTokenClient: func() (authTokenClient, error) {
			tokenCalls++
			return &fakeAuthTokenClient{token: fmt.Sprintf("token-%d", tokenCalls)}, nil
		},
		newAIChatClient: func() (aiChatClient, error) {
			return client, nil
		},
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if client.createToken != "token-1" || client.getToken != "" {
		t.Fatalf("create token = %q, get token = %q", client.createToken, client.getToken)
	}
	if got := strings.Join(client.turnTokens, ","); got != "token-2,token-3" {
		t.Fatalf("turn tokens = %q", got)
	}
	if got := strings.Join(client.turnPrompts, ","); got != "first prompt,second prompt" {
		t.Fatalf("turn prompts = %q", got)
	}
	if tokenCalls != 3 {
		t.Fatalf("token calls = %d", tokenCalls)
	}
	if stdout.String() != "answer: first prompt\nanswer: second prompt\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, text := range []string{"Conversation: conversation-1", "Enter 'exit' or 'quit'", "[tool] get_cluster_health"} {
		if !strings.Contains(stderr.String(), text) {
			t.Fatalf("stderr = %q, want containing %q", stderr.String(), text)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), "token-") {
		t.Fatal("command output exposes a token")
	}
}

func TestCmdAIChatResumesConversationAndExitsOnEOF(t *testing.T) {
	conversation := chatTestConversation("conversation-existing")
	client := &fakeAIChatClient{conversation: conversation}
	var stderr bytes.Buffer
	command := &CmdAIChat{
		ID:      conversation.ID,
		Timeout: time.Second,
		In:      strings.NewReader(""),
		Out:     &bytes.Buffer{},
		ErrOut:  &stderr,
		newAuthTokenClient: func() (authTokenClient, error) {
			return &fakeAuthTokenClient{token: "resume-token"}, nil
		},
		newAIChatClient: func() (aiChatClient, error) {
			return client, nil
		},
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if client.createToken != "" || client.getToken != "resume-token" || client.getID != conversation.ID {
		t.Fatalf("create token = %q, get token = %q, get ID = %q", client.createToken, client.getToken, client.getID)
	}
	if len(client.turnPrompts) != 0 {
		t.Fatalf("turn prompts = %#v", client.turnPrompts)
	}
	if !strings.Contains(stderr.String(), "Conversation: "+conversation.ID) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCmdAIChatInterruptCancelsOnlyActiveTurn(t *testing.T) {
	conversation := chatTestConversation("conversation-1")
	started := make(chan struct{})
	client := &fakeAIChatClient{
		conversation: conversation,
		turn: func(ctx context.Context, _ clientai.EmitFunc) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	interrupt := make(chan struct{}, 1)
	var stderr bytes.Buffer
	command := &CmdAIChat{
		Timeout:   time.Minute,
		In:        strings.NewReader("wait\nexit\n"),
		Out:       &bytes.Buffer{},
		ErrOut:    &stderr,
		Interrupt: interrupt,
		newAuthTokenClient: func() (authTokenClient, error) {
			return &fakeAuthTokenClient{token: "token"}, nil
		},
		newAIChatClient: func() (aiChatClient, error) {
			return client, nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Run(t.Context())
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("conversation turn did not start")
	}
	interrupt <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run command: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("command did not continue after interruption")
	}
	if got := strings.Join(client.turnPrompts, ","); got != "wait" {
		t.Fatalf("turn prompts = %q", got)
	}
	if !strings.Contains(stderr.String(), "[turn canceled]") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCmdAIChatPropagatesSafeFailures(t *testing.T) {
	target := errors.New("agent unavailable")
	for _, test := range []struct {
		name       string
		command    CmdAIChat
		wantTarget error
		wantText   string
	}{
		{
			name:     "short timeout",
			command:  CmdAIChat{Timeout: time.Millisecond},
			wantText: "timeout must be",
		},
		{
			name: "client factory",
			command: CmdAIChat{
				Timeout: time.Second,
				newAIChatClient: func() (aiChatClient, error) {
					return nil, target
				},
			},
			wantTarget: target,
		},
		{
			name: "conversation create",
			command: CmdAIChat{
				Timeout: time.Second,
				In:      strings.NewReader(""),
				newAuthTokenClient: func() (authTokenClient, error) {
					return &fakeAuthTokenClient{token: "token"}, nil
				},
				newAIChatClient: func() (aiChatClient, error) {
					return &fakeAIChatClient{err: target}, nil
				},
			},
			wantTarget: target,
			wantText:   "create AI conversation",
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

func chatTestConversation(id string) clientai.Conversation {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	return clientai.Conversation{
		ID: id, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
}
