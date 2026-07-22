package om

import (
	"testing"
	"time"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
	"github.com/opensvc/om3/v3/core/omcmd"
)

func TestAIAskCommandContract(t *testing.T) {
	cmd := newCmdAIAsk()
	if cmd.Use != "ask PROMPT" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("command accepted an empty prompt")
	}
	if err := cmd.Args(cmd, []string{"health", "of", "cluster"}); err != nil {
		t.Fatalf("command rejected prompt words: %v", err)
	}
	if got := cmd.Flag("agent-url").Value.String(); got != clientai.DefaultEndpoint {
		t.Fatalf("agent-url default = %q", got)
	}
	if got, err := time.ParseDuration(cmd.Flag("timeout").Value.String()); err != nil || got != omcmd.DefaultAIAskTimeout {
		t.Fatalf("timeout default = %q, %v", cmd.Flag("timeout").Value.String(), err)
	}
}
