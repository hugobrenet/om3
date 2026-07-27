package omcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/opensvc/om3/v3/core/client"
	clientai "github.com/opensvc/om3/v3/core/client/ai"
	"github.com/opensvc/om3/v3/daemon/api"
)

const (
	DefaultAIAskTimeout = 10 * time.Minute
	minimumAIAskTimeout = time.Second
	maximumAIAskTimeout = 30 * time.Minute
	tokenValidityMargin = time.Minute
)

var ErrCmdAIAsk = errors.New("command ai ask")

type authTokenClient interface {
	PostAuthTokenWithResponse(context.Context, *api.PostAuthTokenParams, ...api.RequestEditorFn) (*api.PostAuthTokenResponse, error)
}

type aiAgentClient interface {
	Ask(context.Context, string, string, clientai.EmitFunc) (string, error)
}

type CmdAIAsk struct {
	Prompt  string
	Timeout time.Duration
	Out     io.Writer
	ErrOut  io.Writer

	newAuthTokenClient func() (authTokenClient, error)
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
	if t.Timeout < minimumAIAskTimeout || t.Timeout > maximumAIAskTimeout {
		return fmt.Errorf("timeout must be between %s and %s", minimumAIAskTimeout, maximumAIAskTimeout)
	}
	if t.Out == nil {
		t.Out = os.Stdout
	}
	if t.ErrOut == nil {
		t.ErrOut = os.Stderr
	}
	if t.newAuthTokenClient == nil {
		t.newAuthTokenClient = func() (authTokenClient, error) {
			return client.New()
		}
	}
	if t.newAIAgentClient == nil {
		t.newAIAgentClient = func() (aiAgentClient, error) {
			return clientai.New()
		}
	}

	ctx, cancel := context.WithTimeout(parent, t.Timeout)
	defer cancel()
	tokenClient, err := t.newAuthTokenClient()
	if err != nil {
		return fmt.Errorf("create local daemon client: %w", err)
	}
	tokenDuration := (t.Timeout + tokenValidityMargin).String()
	tokenResponse, err := tokenClient.PostAuthTokenWithResponse(ctx, &api.PostAuthTokenParams{AccessDuration: &tokenDuration})
	if err != nil {
		return fmt.Errorf("create AI access token: %w", err)
	}
	if tokenResponse == nil {
		return fmt.Errorf("create AI access token: daemon returned an empty response")
	}
	if tokenResponse.StatusCode() != http.StatusOK || tokenResponse.JSON200 == nil {
		return fmt.Errorf("create AI access token: daemon returned HTTP %d", tokenResponse.StatusCode())
	}
	token := tokenResponse.JSON200.AccessToken
	if token == "" {
		return fmt.Errorf("create AI access token: daemon returned an empty token")
	}
	agentClient, err := t.newAIAgentClient()
	if err != nil {
		return fmt.Errorf("create AI agent client: %w", err)
	}

	printedText := false
	_, err = agentClient.Ask(ctx, token, t.Prompt, func(event clientai.Event) error {
		switch event.Type {
		case "text_delta":
			printedText = true
			if _, err := io.WriteString(t.Out, event.TextDelta); err != nil {
				return fmt.Errorf("write AI response: %w", err)
			}
		case "tool_started":
			if _, err := fmt.Fprintf(t.ErrOut, "[tool] %s\n", event.ToolName); err != nil {
				return fmt.Errorf("write AI tool progress: %w", err)
			}
		case "tool_finished":
			if event.ToolError != nil && *event.ToolError {
				if _, err := fmt.Fprintf(t.ErrOut, "[tool] %s failed\n", event.ToolName); err != nil {
					return fmt.Errorf("write AI tool progress: %w", err)
				}
			}
		}
		return nil
	})
	if printedText {
		_, _ = fmt.Fprintln(t.Out)
	}
	if err != nil {
		return err
	}
	return nil
}
