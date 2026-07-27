package omcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
	"github.com/opensvc/om3/v3/core/output"
	"github.com/opensvc/om3/v3/core/rawconfig"
	"github.com/opensvc/om3/v3/util/unstructured"
)

const (
	DefaultAIConversationTimeout = 30 * time.Second
	minimumAIConversationTimeout = time.Second
	maximumAIConversationTimeout = 2 * time.Minute
	conversationTableColumns     = "ID:id,CREATED_AT:created_at,UPDATED_AT:updated_at,EXPIRES_AT:expires_at,STORED_BYTES:stored_bytes"
)

type aiConversationClient interface {
	ListConversations(context.Context, string) ([]clientai.Conversation, error)
	GetConversation(context.Context, string, string) (clientai.Conversation, error)
	DeleteConversation(context.Context, string, string) error
}

type aiConversationClientFactory func() (aiConversationClient, error)

type OptsAIConversation struct {
	Timeout time.Duration
	Output  string
	Color   string
	Out     io.Writer

	newAuthTokenClient      authTokenClientFactory
	newAIConversationClient aiConversationClientFactory
}

type conversationListOutput struct {
	Conversations []clientai.Conversation `json:"conversations"`
}

func (o conversationListOutput) GetItems() any {
	return conversationItems(o.Conversations)
}

type conversationOutput struct {
	Conversation clientai.Conversation `json:"conversation"`
}

func (o conversationOutput) GetItems() any {
	return conversationItems([]clientai.Conversation{o.Conversation})
}

func conversationItems(items []clientai.Conversation) unstructured.List {
	result := make(unstructured.List, len(items))
	for index, item := range items {
		result[index] = map[string]any{
			"id":           item.ID,
			"created_at":   item.CreatedAt,
			"updated_at":   item.UpdatedAt,
			"expires_at":   item.ExpiresAt,
			"stored_bytes": item.StoredBytes,
		}
	}
	return result
}

func (o *OptsAIConversation) prepare(parent context.Context) (context.Context, context.CancelFunc, string, aiConversationClient, error) {
	if o.Timeout < minimumAIConversationTimeout || o.Timeout > maximumAIConversationTimeout {
		return nil, nil, "", nil, fmt.Errorf("timeout must be between %s and %s", minimumAIConversationTimeout, maximumAIConversationTimeout)
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Output == "" {
		o.Output = "auto"
	}
	if o.Color == "" {
		o.Color = "auto"
	}
	if o.newAIConversationClient == nil {
		o.newAIConversationClient = func() (aiConversationClient, error) {
			return clientai.New()
		}
	}

	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	client, err := o.newAIConversationClient()
	if err != nil {
		cancel()
		return nil, nil, "", nil, fmt.Errorf("create AI agent client: %w", err)
	}
	token, err := issueAIAccessToken(ctx, o.Timeout, o.newAuthTokenClient)
	if err != nil {
		cancel()
		return nil, nil, "", nil, err
	}
	return ctx, cancel, token, client, nil
}

func (o *OptsAIConversation) render(data any) error {
	text, err := (output.Renderer{
		DefaultOutput: "tab=" + conversationTableColumns,
		Output:        o.Output,
		Color:         o.Color,
		Data:          data,
		Colorize:      rawconfig.Colorize,
	}).Sprint()
	if err != nil {
		return fmt.Errorf("render AI conversation output: %w", err)
	}
	if _, err := io.WriteString(o.Out, text); err != nil {
		return fmt.Errorf("write AI conversation output: %w", err)
	}
	return nil
}
