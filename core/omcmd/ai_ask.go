package omcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
)

const (
	DefaultAIAskTimeout = 10 * time.Minute
)

var ErrCmdAIAsk = errors.New("command ai ask")

type aiAgentClient interface {
	Ask(context.Context, string, string, clientai.EmitFunc) (string, error)
}

type CmdAIAsk struct {
	Prompt  string
	Timeout time.Duration
	Out     io.Writer
	ErrOut  io.Writer

	newAuthTokenClient authTokenClientFactory
	newAIAgentClient   func() (aiAgentClient, error)
}

func (t *CmdAIAsk) Run(ctx context.Context) error {
	if err := t.run(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCmdAIAsk, err)
	}
	return nil
}

func (t *CmdAIAsk) run(parent context.Context) error {
	if strings.TrimSpace(t.Prompt) == "" {
		return fmt.Errorf("prompt is empty")
	}
	if err := validateAITurnTimeout(t.Timeout); err != nil {
		return err
	}
	if t.Out == nil {
		t.Out = os.Stdout
	}
	if t.ErrOut == nil {
		t.ErrOut = os.Stderr
	}
	if t.newAIAgentClient == nil {
		t.newAIAgentClient = func() (aiAgentClient, error) {
			return clientai.New()
		}
	}

	ctx, cancel := context.WithTimeout(parent, t.Timeout)
	defer cancel()
	token, err := issueAIAccessToken(ctx, t.Timeout, t.newAuthTokenClient)
	if err != nil {
		return err
	}
	agentClient, err := t.newAIAgentClient()
	if err != nil {
		return fmt.Errorf("create AI agent client: %w", err)
	}

	stream := newAIStreamWriter(t.Out, t.ErrOut)
	_, err = agentClient.Ask(ctx, token, t.Prompt, stream.emit)
	stream.finish()
	if err != nil {
		return err
	}
	return nil
}
