package omcmd

import (
	"context"
	"errors"
	"fmt"
)

var ErrCmdAIList = errors.New("command ai list")

type CmdAIList struct {
	OptsAIConversation
}

func (t *CmdAIList) Run(ctx context.Context) error {
	if err := t.run(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCmdAIList, err)
	}
	return nil
}

func (t *CmdAIList) run(parent context.Context) error {
	ctx, cancel, token, client, err := t.prepare(parent)
	if err != nil {
		return err
	}
	defer cancel()
	items, err := client.ListConversations(ctx, token)
	if err != nil {
		return err
	}
	return t.render(conversationListOutput{Conversations: items})
}
