package omcmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
)

const (
	DefaultAIChatTurnTimeout = DefaultAIAskTimeout
	maxAIChatInputBytes      = 32<<10 + 2
)

var (
	ErrCmdAIChat          = errors.New("command ai chat")
	errAIChatSessionEnded = errors.New("AI chat session ended")
)

type aiChatClient interface {
	CreateConversation(context.Context, string) (clientai.Conversation, error)
	GetConversation(context.Context, string, string) (clientai.Conversation, error)
	ListConversations(context.Context, string) ([]clientai.Conversation, error)
	SendConversationTurn(context.Context, string, string, string, clientai.EmitFunc) (string, error)
}

type CmdAIChat struct {
	ID        string
	Resume    bool
	Timeout   time.Duration
	In        io.Reader
	Out       io.Writer
	ErrOut    io.Writer
	Interrupt <-chan struct{}

	newAuthTokenClient authTokenClientFactory
	newAIChatClient    func() (aiChatClient, error)
	now                func() time.Time
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
	if t.Resume && strings.TrimSpace(t.ID) != "" {
		return fmt.Errorf("conversation ID and --resume are mutually exclusive")
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
	if t.now == nil {
		t.now = time.Now
	}
	client, err := t.newAIChatClient()
	if err != nil {
		return fmt.Errorf("create AI agent client: %w", err)
	}

	sessionCtx, cancelSession := context.WithCancel(parent)
	defer cancelSession()
	input := scanAIChatInput(sessionCtx, t.In)
	conversation, err := t.openConversation(parent, client, input)
	if err != nil {
		if errors.Is(err, errAIChatSessionEnded) {
			return nil
		}
		return err
	}
	if _, err := fmt.Fprintf(t.ErrOut, "Conversation: %s\n", conversation.ID); err != nil {
		return fmt.Errorf("write AI conversation identifier: %w", err)
	}
	if _, err := fmt.Fprintln(t.ErrOut, "Enter 'exit' or 'quit' to end the session."); err != nil {
		return fmt.Errorf("write AI session help: %w", err)
	}

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

func (t *CmdAIChat) openConversation(parent context.Context, client aiChatClient, input <-chan aiChatInput) (clientai.Conversation, error) {
	id := strings.TrimSpace(t.ID)
	if t.Resume {
		items, err := t.listConversations(parent, client)
		if err != nil {
			return clientai.Conversation{}, err
		}
		selected, err := t.selectConversation(parent, input, items)
		if err != nil {
			return clientai.Conversation{}, err
		}
		return t.getConversation(parent, client, selected.ID)
	}
	if id != "" {
		return t.getConversation(parent, client, id)
	}
	return t.createConversation(parent, client)
}

func (t *CmdAIChat) createConversation(parent context.Context, client aiChatClient) (clientai.Conversation, error) {
	ctx, cancel := context.WithTimeout(parent, DefaultAIConversationTimeout)
	defer cancel()
	token, err := issueAIAccessToken(ctx, DefaultAIConversationTimeout, t.newAuthTokenClient)
	if err != nil {
		return clientai.Conversation{}, err
	}
	item, err := client.CreateConversation(ctx, token)
	if err != nil {
		return clientai.Conversation{}, fmt.Errorf("create AI conversation: %w", err)
	}
	return item, nil
}

func (t *CmdAIChat) getConversation(parent context.Context, client aiChatClient, id string) (clientai.Conversation, error) {
	ctx, cancel := context.WithTimeout(parent, DefaultAIConversationTimeout)
	defer cancel()
	token, err := issueAIAccessToken(ctx, DefaultAIConversationTimeout, t.newAuthTokenClient)
	if err != nil {
		return clientai.Conversation{}, err
	}
	item, err := client.GetConversation(ctx, token, id)
	if err != nil {
		return clientai.Conversation{}, fmt.Errorf("resume AI conversation: %w", err)
	}
	return item, nil
}

func (t *CmdAIChat) listConversations(parent context.Context, client aiChatClient) ([]clientai.Conversation, error) {
	ctx, cancel := context.WithTimeout(parent, DefaultAIConversationTimeout)
	defer cancel()
	token, err := issueAIAccessToken(ctx, DefaultAIConversationTimeout, t.newAuthTokenClient)
	if err != nil {
		return nil, err
	}
	items, err := client.ListConversations(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("list AI conversations: %w", err)
	}
	return items, nil
}

func (t *CmdAIChat) selectConversation(parent context.Context, input <-chan aiChatInput, items []clientai.Conversation) (clientai.Conversation, error) {
	if len(items) == 0 {
		return clientai.Conversation{}, fmt.Errorf("no persistent AI conversations are available")
	}
	if _, err := fmt.Fprintln(t.ErrOut, "Select a conversation:"); err != nil {
		return clientai.Conversation{}, fmt.Errorf("write AI conversation selection: %w", err)
	}
	if _, err := fmt.Fprintln(t.ErrOut); err != nil {
		return clientai.Conversation{}, fmt.Errorf("write AI conversation selection: %w", err)
	}
	now := t.now().UTC()
	for index, item := range items {
		title := item.Title
		if title == "" {
			title = untitledConversationLabel
		}
		if _, err := fmt.Fprintf(t.ErrOut, "  %d. %s  updated %s  %s\n", index+1, title, formatConversationAge(now, item.UpdatedAt), shortConversationID(item.ID)); err != nil {
			return clientai.Conversation{}, fmt.Errorf("write AI conversation selection: %w", err)
		}
	}
	if _, err := fmt.Fprintln(t.ErrOut); err != nil {
		return clientai.Conversation{}, fmt.Errorf("write AI conversation selection: %w", err)
	}
	for {
		if _, err := io.WriteString(t.ErrOut, "Conversation [1]: "); err != nil {
			return clientai.Conversation{}, fmt.Errorf("write AI conversation selection prompt: %w", err)
		}
		select {
		case <-parent.Done():
			return clientai.Conversation{}, parent.Err()
		case <-t.Interrupt:
			if _, err := fmt.Fprintln(t.ErrOut); err != nil {
				return clientai.Conversation{}, fmt.Errorf("write AI conversation selection interruption: %w", err)
			}
			continue
		case result, ok := <-input:
			if !ok {
				_, _ = fmt.Fprintln(t.ErrOut)
				return clientai.Conversation{}, errAIChatSessionEnded
			}
			if result.err != nil {
				return clientai.Conversation{}, fmt.Errorf("read AI conversation selection: %w", result.err)
			}
			selection := strings.TrimSpace(result.line)
			if selection == "exit" || selection == "quit" {
				return clientai.Conversation{}, errAIChatSessionEnded
			}
			if selection == "" {
				return items[0], nil
			}
			index, err := strconv.Atoi(selection)
			if err == nil && index >= 1 && index <= len(items) {
				return items[index-1], nil
			}
			if _, err := fmt.Fprintf(t.ErrOut, "Invalid selection. Enter a number between 1 and %d.\n", len(items)); err != nil {
				return clientai.Conversation{}, fmt.Errorf("write AI conversation selection error: %w", err)
			}
		}
	}
}

func shortConversationID(id string) string {
	const length = 8
	if len(id) <= length {
		return id
	}
	return id[:length]
}

func formatConversationAge(now time.Time, updatedAt time.Time) string {
	age := now.Sub(updatedAt.UTC())
	if age < 0 {
		return updatedAt.UTC().Format(time.RFC3339)
	}
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return pluralAge(int(age/time.Minute), "minute")
	case age < 24*time.Hour:
		return pluralAge(int(age/time.Hour), "hour")
	default:
		return pluralAge(int(age/(24*time.Hour)), "day")
	}
}

func pluralAge(value int, unit string) string {
	if value != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s ago", value, unit)
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
