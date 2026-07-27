package omcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrCmdAIDelete = errors.New("command ai delete")

type CmdAIDelete struct {
	OptsAIConversation
	ID string
}

func (t *CmdAIDelete) Run(ctx context.Context) error {
	if err := t.run(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCmdAIDelete, err)
	}
	return nil
}

func (t *CmdAIDelete) run(parent context.Context) error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("conversation ID is empty")
	}
	ctx, cancel, token, client, err := t.prepare(parent)
	if err != nil {
		return err
	}
	defer cancel()
	return client.DeleteConversation(ctx, token, t.ID)
}
