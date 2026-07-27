package omcmd

import (
	"fmt"
	"io"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
)

const (
	minimumAITurnTimeout = time.Second
	maximumAITurnTimeout = 30 * time.Minute
)

func validateAITurnTimeout(timeout time.Duration) error {
	if timeout < minimumAITurnTimeout || timeout > maximumAITurnTimeout {
		return fmt.Errorf("timeout must be between %s and %s", minimumAITurnTimeout, maximumAITurnTimeout)
	}
	return nil
}

type aiStreamWriter struct {
	out         io.Writer
	errOut      io.Writer
	printedText bool
}

func newAIStreamWriter(out io.Writer, errOut io.Writer) *aiStreamWriter {
	return &aiStreamWriter{out: out, errOut: errOut}
}

func (w *aiStreamWriter) emit(event clientai.Event) error {
	switch event.Type {
	case "text_delta":
		w.printedText = true
		if _, err := io.WriteString(w.out, event.TextDelta); err != nil {
			return fmt.Errorf("write AI response: %w", err)
		}
	case "tool_started":
		if _, err := fmt.Fprintf(w.errOut, "[tool] %s\n", event.ToolName); err != nil {
			return fmt.Errorf("write AI tool progress: %w", err)
		}
	case "tool_finished":
		if event.ToolError != nil && *event.ToolError {
			if _, err := fmt.Fprintf(w.errOut, "[tool] %s failed\n", event.ToolName); err != nil {
				return fmt.Errorf("write AI tool progress: %w", err)
			}
		}
	}
	return nil
}

func (w *aiStreamWriter) finish() {
	if w.printedText {
		_, _ = fmt.Fprintln(w.out)
	}
}
