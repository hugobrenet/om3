package omcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
)

type fakeAIConversationClient struct {
	items       []clientai.Conversation
	item        clientai.Conversation
	listErr     error
	getErr      error
	deleteErr   error
	updateErr   error
	token       string
	getID       string
	deleteID    string
	updateID    string
	updateTitle string
	hasDeadline bool
}

func (c *fakeAIConversationClient) ListConversations(ctx context.Context, token string) ([]clientai.Conversation, error) {
	c.token = token
	_, c.hasDeadline = ctx.Deadline()
	return c.items, c.listErr
}

func (c *fakeAIConversationClient) GetConversation(ctx context.Context, token string, id string) (clientai.Conversation, error) {
	c.token = token
	c.getID = id
	_, c.hasDeadline = ctx.Deadline()
	return c.item, c.getErr
}

func (c *fakeAIConversationClient) DeleteConversation(ctx context.Context, token string, id string) error {
	c.token = token
	c.deleteID = id
	_, c.hasDeadline = ctx.Deadline()
	return c.deleteErr
}

func (c *fakeAIConversationClient) UpdateConversationTitle(ctx context.Context, token string, id string, title string) (clientai.Conversation, error) {
	c.token = token
	c.updateID = id
	c.updateTitle = title
	_, c.hasDeadline = ctx.Deadline()
	return c.item, c.updateErr
}

func TestCmdAIListRendersConversations(t *testing.T) {
	items := []clientai.Conversation{
		testAIConversation("conversation-1", 1024),
		testAIConversation("conversation-2", 2048),
	}
	for _, test := range []struct {
		name   string
		output string
		check  func(*testing.T, string)
	}{
		{
			name: "table",
			check: func(t *testing.T, value string) {
				for _, expected := range []string{"TITLE", "ID", "UPDATED_AT", "STORED_BYTES", "Cluster health conversation-1", "conversation-2"} {
					if !strings.Contains(value, expected) {
						t.Fatalf("output %q does not contain %q", value, expected)
					}
				}
			},
		},
		{
			name:   "JSON",
			output: "json",
			check: func(t *testing.T, value string) {
				var payload conversationListOutput
				if err := json.Unmarshal([]byte(value), &payload); err != nil {
					t.Fatalf("decode output: %v", err)
				}
				if len(payload.Conversations) != 2 || payload.Conversations[0].ID != "conversation-1" {
					t.Fatalf("output = %#v", payload)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenClient := &fakeAuthTokenClient{token: "access-token"}
			agentClient := &fakeAIConversationClient{items: items}
			var stdout bytes.Buffer
			command := &CmdAIList{OptsAIConversation: testAIConversationOptions(&stdout, test.output, tokenClient, agentClient)}
			if err := command.Run(t.Context()); err != nil {
				t.Fatalf("run command: %v", err)
			}
			if agentClient.token != "access-token" || !agentClient.hasDeadline {
				t.Fatalf("agent token=%q deadline=%v", agentClient.token, agentClient.hasDeadline)
			}
			test.check(t, stdout.String())
		})
	}
}

func TestCmdAIRenameUpdatesAndRendersConversation(t *testing.T) {
	const (
		id    = "conversation-1"
		title = "Renamed incident"
	)
	item := testAIConversation(id, 1024)
	item.Title = title
	tokenClient := &fakeAuthTokenClient{token: "access-token"}
	agentClient := &fakeAIConversationClient{item: item}
	var stdout bytes.Buffer
	command := &CmdAIRename{
		OptsAIConversation: testAIConversationOptions(&stdout, "json", tokenClient, agentClient),
		ID:                 id,
		Title:              title,
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if agentClient.updateID != id || agentClient.updateTitle != title || agentClient.token != "access-token" || !agentClient.hasDeadline {
		t.Fatalf("update ID=%q title=%q token=%q deadline=%v", agentClient.updateID, agentClient.updateTitle, agentClient.token, agentClient.hasDeadline)
	}
	var payload conversationOutput
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload.Conversation != item {
		t.Fatalf("output = %#v, want %#v", payload.Conversation, item)
	}
}

func TestConversationTableUsesUntitledFallback(t *testing.T) {
	item := testAIConversation("conversation-1", 0)
	item.Title = ""
	items := conversationItems([]clientai.Conversation{item})
	if len(items) != 1 || items[0]["title"] != untitledConversationLabel {
		t.Fatalf("items = %#v", items)
	}
}

func TestCmdAIShowRendersConversation(t *testing.T) {
	const id = "conversation-1"
	item := testAIConversation(id, 1024)
	tokenClient := &fakeAuthTokenClient{token: "access-token"}
	agentClient := &fakeAIConversationClient{item: item}
	var stdout bytes.Buffer
	command := &CmdAIShow{
		OptsAIConversation: testAIConversationOptions(&stdout, "json", tokenClient, agentClient),
		ID:                 id,
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if agentClient.getID != id || agentClient.token != "access-token" || !agentClient.hasDeadline {
		t.Fatalf("get ID=%q token=%q deadline=%v", agentClient.getID, agentClient.token, agentClient.hasDeadline)
	}
	var payload conversationOutput
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload.Conversation != item {
		t.Fatalf("output = %#v, want %#v", payload.Conversation, item)
	}
}

func TestCmdAIDeleteDeletesConversationWithoutOutput(t *testing.T) {
	const id = "conversation-1"
	tokenClient := &fakeAuthTokenClient{token: "access-token"}
	agentClient := &fakeAIConversationClient{}
	var stdout bytes.Buffer
	command := &CmdAIDelete{
		OptsAIConversation: testAIConversationOptions(&stdout, "", tokenClient, agentClient),
		ID:                 id,
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run command: %v", err)
	}
	if agentClient.deleteID != id || agentClient.token != "access-token" || !agentClient.hasDeadline {
		t.Fatalf("delete ID=%q token=%q deadline=%v", agentClient.deleteID, agentClient.token, agentClient.hasDeadline)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestAIConversationCommandsReturnWrappedFailures(t *testing.T) {
	target := errors.New("target failure")
	tests := []struct {
		name       string
		run        func(context.Context) error
		wantTarget error
		wantText   string
	}{
		{
			name: "list timeout",
			run: func(ctx context.Context) error {
				return (&CmdAIList{}).Run(ctx)
			},
			wantTarget: ErrCmdAIList,
			wantText:   "timeout must be",
		},
		{
			name: "show empty ID",
			run: func(ctx context.Context) error {
				return (&CmdAIShow{}).Run(ctx)
			},
			wantTarget: ErrCmdAIShow,
			wantText:   "conversation ID is empty",
		},
		{
			name: "delete empty ID",
			run: func(ctx context.Context) error {
				return (&CmdAIDelete{}).Run(ctx)
			},
			wantTarget: ErrCmdAIDelete,
			wantText:   "conversation ID is empty",
		},
		{
			name: "rename empty ID",
			run: func(ctx context.Context) error {
				return (&CmdAIRename{Title: "title"}).Run(ctx)
			},
			wantTarget: ErrCmdAIRename,
			wantText:   "conversation ID is empty",
		},
		{
			name: "rename empty title",
			run: func(ctx context.Context) error {
				return (&CmdAIRename{ID: "conversation-1"}).Run(ctx)
			},
			wantTarget: ErrCmdAIRename,
			wantText:   "conversation title is empty",
		},
		{
			name: "agent client",
			run: func(ctx context.Context) error {
				command := &CmdAIList{OptsAIConversation: OptsAIConversation{
					Timeout: time.Second,
					newAIConversationClient: func() (aiConversationClient, error) {
						return nil, target
					},
				}}
				return command.Run(ctx)
			},
			wantTarget: target,
		},
		{
			name: "list request",
			run: func(ctx context.Context) error {
				command := &CmdAIList{OptsAIConversation: testAIConversationOptions(&bytes.Buffer{}, "", &fakeAuthTokenClient{token: "token"}, &fakeAIConversationClient{listErr: target})}
				return command.Run(ctx)
			},
			wantTarget: target,
		},
		{
			name: "show request",
			run: func(ctx context.Context) error {
				command := &CmdAIShow{
					OptsAIConversation: testAIConversationOptions(&bytes.Buffer{}, "", &fakeAuthTokenClient{token: "token"}, &fakeAIConversationClient{getErr: target}),
					ID:                 "conversation-1",
				}
				return command.Run(ctx)
			},
			wantTarget: target,
		},
		{
			name: "delete request",
			run: func(ctx context.Context) error {
				command := &CmdAIDelete{
					OptsAIConversation: testAIConversationOptions(&bytes.Buffer{}, "", &fakeAuthTokenClient{token: "token"}, &fakeAIConversationClient{deleteErr: target}),
					ID:                 "conversation-1",
				}
				return command.Run(ctx)
			},
			wantTarget: target,
		},
		{
			name: "rename request",
			run: func(ctx context.Context) error {
				command := &CmdAIRename{
					OptsAIConversation: testAIConversationOptions(&bytes.Buffer{}, "", &fakeAuthTokenClient{token: "token"}, &fakeAIConversationClient{updateErr: target}),
					ID:                 "conversation-1",
					Title:              "title",
				}
				return command.Run(ctx)
			},
			wantTarget: target,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(t.Context())
			if err == nil {
				t.Fatal("command succeeded")
			}
			if !errors.Is(err, test.wantTarget) {
				t.Fatalf("error = %v, want wrapping %v", err, test.wantTarget)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want containing %q", err, test.wantText)
			}
		})
	}
}

func testAIConversationOptions(out *bytes.Buffer, output string, tokenClient authTokenClient, agentClient aiConversationClient) OptsAIConversation {
	return OptsAIConversation{
		Timeout: 2 * time.Second,
		Output:  output,
		Color:   "no",
		Out:     out,
		newAuthTokenClient: func() (authTokenClient, error) {
			return tokenClient, nil
		},
		newAIConversationClient: func() (aiConversationClient, error) {
			return agentClient, nil
		},
	}
}

func testAIConversation(id string, storedBytes int64) clientai.Conversation {
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	return clientai.Conversation{
		ID: id, Title: "Cluster health " + id, CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Minute),
		ExpiresAt: createdAt.Add(7 * 24 * time.Hour), StoredBytes: storedBytes,
	}
}
