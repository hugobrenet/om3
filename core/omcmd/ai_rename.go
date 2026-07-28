package omcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrCmdAIRename = errors.New("command ai rename")

type CmdAIRename struct {
	OptsAIConversation
	ID    string
	Title string
}

func (t *CmdAIRename) Run(ctx context.Context) error {
	if err := t.run(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCmdAIRename, err)
	}
	return nil
}

func (t *CmdAIRename) run(parent context.Context) error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("conversation ID is empty")
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("conversation title is empty")
	}
	ctx, cancel, token, client, err := t.prepare(parent)
	if err != nil {
		return err
	}
	defer cancel()
	item, err := client.UpdateConversationTitle(ctx, token, t.ID, t.Title)
	if err != nil {
		return err
	}
	return t.render(conversationOutput{Conversation: item})
}
