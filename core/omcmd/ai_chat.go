package omcmd

import (
	"bufio"
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
	DefaultAIChatTurnTimeout = DefaultAIAskTimeout
	maxAIChatInputBytes      = 32<<10 + 2
)

var ErrCmdAIChat = errors.New("command ai chat")

type aiChatClient interface {
	CreateConversation(context.Context, string) (clientai.Conversation, error)
	GetConversation(context.Context, string, string) (clientai.Conversation, error)
	SendConversationTurn(context.Context, string, string, string, clientai.EmitFunc) (string, error)
}

type CmdAIChat struct {
	ID        string
	Timeout   time.Duration
	In        io.Reader
	Out       io.Writer
	ErrOut    io.Writer
	Interrupt <-chan struct{}

	newAuthTokenClient authTokenClientFactory
	newAIChatClient    func() (aiChatClient, error)
}

func (t *CmdAIChat) Run(ctx context.Context) error {
	if err := t.run(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCmdAIChat, err)
	}
	return nil
}

func (t *CmdAIChat) run(parent context.Context) error {
	if err := validateAITurnTimeout(t.Timeout); err != nil {
		return err
	}
	if t.In == nil {
		t.In = os.Stdin
	}
	if t.Out == nil {
		t.Out = os.Stdout
	}
	if t.ErrOut == nil {
		t.ErrOut = os.Stderr
	}
	if t.newAIChatClient == nil {
		t.newAIChatClient = func() (aiChatClient, error) {
			return clientai.New()
		}
	}
	client, err := t.newAIChatClient()
	if err != nil {
		return fmt.Errorf("create AI agent client: %w", err)
	}

	conversation, err := t.openConversation(parent, client)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(t.ErrOut, "Conversation: %s\n", conversation.ID); err != nil {
		return fmt.Errorf("write AI conversation identifier: %w", err)
	}
	if _, err := fmt.Fprintln(t.ErrOut, "Enter 'exit' or 'quit' to end the session."); err != nil {
		return fmt.Errorf("write AI session help: %w", err)
	}

	sessionCtx, cancelSession := context.WithCancel(parent)
	defer cancelSession()
	input := scanAIChatInput(sessionCtx, t.In)
	for {
		if _, err := io.WriteString(t.ErrOut, "> "); err != nil {
			return fmt.Errorf("write AI prompt: %w", err)
		}
		select {
		case <-parent.Done():
			return parent.Err()
		case <-t.Interrupt:
			if _, err := fmt.Fprintln(t.ErrOut); err != nil {
				return fmt.Errorf("write AI prompt interruption: %w", err)
			}
			continue
		case result, ok := <-input:
			if !ok {
				if _, err := fmt.Fprintln(t.ErrOut); err != nil {
					return fmt.Errorf("write AI session end: %w", err)
				}
				return nil
			}
			if result.err != nil {
				return fmt.Errorf("read AI prompt: %w", result.err)
			}
			prompt := strings.TrimSpace(result.line)
			if prompt == "" {
				continue
			}
			if prompt == "exit" || prompt == "quit" {
				return nil
			}
			interrupted, err := t.runTurn(parent, client, conversation.ID, prompt)
			if interrupted {
				if _, writeErr := fmt.Fprintln(t.ErrOut, "\n[turn canceled]"); writeErr != nil {
					return fmt.Errorf("write AI turn cancellation: %w", writeErr)
				}
				continue
			}
			if err != nil {
				return err
			}
		}
	}
}

func (t *CmdAIChat) openConversation(parent context.Context, client aiChatClient) (clientai.Conversation, error) {
	ctx, cancel := context.WithTimeout(parent, DefaultAIConversationTimeout)
	defer cancel()
	token, err := issueAIAccessToken(ctx, DefaultAIConversationTimeout, t.newAuthTokenClient)
	if err != nil {
		return clientai.Conversation{}, err
	}
	id := strings.TrimSpace(t.ID)
	if id == "" {
		conversation, err := client.CreateConversation(ctx, token)
		if err != nil {
			return clientai.Conversation{}, fmt.Errorf("create AI conversation: %w", err)
		}
		return conversation, nil
	}
	conversation, err := client.GetConversation(ctx, token, id)
	if err != nil {
		return clientai.Conversation{}, fmt.Errorf("resume AI conversation: %w", err)
	}
	return conversation, nil
}

func (t *CmdAIChat) runTurn(parent context.Context, client aiChatClient, conversationID string, prompt string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, t.Timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		token, err := issueAIAccessToken(ctx, t.Timeout, t.newAuthTokenClient)
		if err == nil {
			stream := newAIStreamWriter(t.Out, t.ErrOut)
			_, err = client.SendConversationTurn(ctx, token, conversationID, prompt, stream.emit)
			stream.finish()
		}
		result <- err
	}()

	select {
	case err := <-result:
		return false, err
	case <-t.Interrupt:
		cancel()
		<-result
		return true, nil
	case <-parent.Done():
		cancel()
		<-result
		return false, parent.Err()
	}
}

type aiChatInput struct {
	line string
	err  error
}

func scanAIChatInput(ctx context.Context, reader io.Reader) <-chan aiChatInput {
	result := make(chan aiChatInput)
	go func() {
		defer close(result)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), maxAIChatInputBytes)
		for scanner.Scan() {
			select {
			case result <- aiChatInput{line: scanner.Text()}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case result <- aiChatInput{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return result
}
