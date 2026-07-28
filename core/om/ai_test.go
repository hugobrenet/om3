package om

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/om3/v3/core/omcmd"
	"github.com/spf13/cobra"
)

func TestAICommandHelpDocumentsSubcommands(t *testing.T) {
	cmd := newCmdAI()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	for _, expected := range []string{
		"ask", "chat", "list", "show", "rename", "delete", "metadata only",
		"OPENSVC_AI_AGENT_URL", "om ai chat CONVERSATION_ID", "om ai show CONVERSATION_ID",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("help output does not contain %q:\n%s", expected, output.String())
		}
	}
}

func TestAIChatCommandContract(t *testing.T) {
	cmd := newCmdAIChat()
	if cmd.Use != "chat [CONVERSATION_ID]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, args := range [][]string{nil, {"conversation-id"}} {
		if err := cmd.Args(cmd, args); err != nil {
			t.Fatalf("command rejected args %#v: %v", args, err)
		}
	}
	if err := cmd.Args(cmd, []string{"first", "second"}); err == nil {
		t.Fatal("command accepted more than one conversation ID")
	}
	if cmd.Flag("agent-url") != nil {
		t.Fatal("agent-url flag is exposed")
	}
	if cmd.Flag("output") != nil {
		t.Fatal("output flag is exposed")
	}
	if got, err := time.ParseDuration(cmd.Flag("timeout").Value.String()); err != nil || got != omcmd.DefaultAIChatTurnTimeout {
		t.Fatalf("timeout default = %q, %v", cmd.Flag("timeout").Value.String(), err)
	}
}

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
	if cmd.Flag("agent-url") != nil {
		t.Fatal("agent-url flag is still exposed")
	}
	if got, err := time.ParseDuration(cmd.Flag("timeout").Value.String()); err != nil || got != omcmd.DefaultAIAskTimeout {
		t.Fatalf("timeout default = %q, %v", cmd.Flag("timeout").Value.String(), err)
	}
}

func TestAIConversationCommandContracts(t *testing.T) {
	tests := []struct {
		name       string
		cmd        *cobra.Command
		validArgs  []string
		invalidArg []string
		wantUse    string
		wantAlias  string
		wantOutput bool
	}{
		{
			name: "list", cmd: newCmdAIList(), validArgs: nil, invalidArg: []string{"id"},
			wantUse: "list", wantAlias: "ls", wantOutput: true,
		},
		{
			name: "show", cmd: newCmdAIShow(), validArgs: []string{"id"}, invalidArg: nil,
			wantUse: "show CONVERSATION_ID", wantOutput: true,
		},
		{
			name: "delete", cmd: newCmdAIDelete(), validArgs: []string{"id"}, invalidArg: nil,
			wantUse: "delete CONVERSATION_ID", wantAlias: "del",
		},
		{
			name: "rename", cmd: newCmdAIRename(), validArgs: []string{"id", "Cluster", "health"}, invalidArg: []string{"id"},
			wantUse: "rename CONVERSATION_ID TITLE", wantOutput: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.cmd.Use != test.wantUse {
				t.Fatalf("Use = %q, want %q", test.cmd.Use, test.wantUse)
			}
			if err := test.cmd.Args(test.cmd, test.validArgs); err != nil {
				t.Fatalf("valid args rejected: %v", err)
			}
			if err := test.cmd.Args(test.cmd, test.invalidArg); err == nil {
				t.Fatalf("invalid args %#v accepted", test.invalidArg)
			}
			if test.wantAlias != "" && !containsString(test.cmd.Aliases, test.wantAlias) {
				t.Fatalf("aliases = %#v, want %q", test.cmd.Aliases, test.wantAlias)
			}
			if got, err := time.ParseDuration(test.cmd.Flag("timeout").Value.String()); err != nil || got != omcmd.DefaultAIConversationTimeout {
				t.Fatalf("timeout default = %q, %v", test.cmd.Flag("timeout").Value.String(), err)
			}
			if got := test.cmd.Flag("output") != nil; got != test.wantOutput {
				t.Fatalf("output flag present = %v, want %v", got, test.wantOutput)
			}
		})
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
