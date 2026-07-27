package omcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrCmdAIShow = errors.New("command ai show")

type CmdAIShow struct {
	OptsAIConversation
	ID string
}

func (t *CmdAIShow) Run(ctx context.Context) error {
	if err := t.run(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCmdAIShow, err)
	}
	return nil
}

func (t *CmdAIShow) run(parent context.Context) error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("conversation ID is empty")
	}
	ctx, cancel, token, client, err := t.prepare(parent)
	if err != nil {
		return err
	}
	defer cancel()
	item, err := client.GetConversation(ctx, token, t.ID)
	if err != nil {
		return err
	}
	return t.render(conversationOutput{Conversation: item})
}
