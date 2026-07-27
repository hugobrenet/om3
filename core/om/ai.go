package om

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/opensvc/om3/v3/core/commoncmd"
	"github.com/opensvc/om3/v3/core/omcmd"
	"github.com/spf13/cobra"
)

func init() {
	root.AddCommand(newCmdAI())
}

func newCmdAI() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "interact with the local OpenSVC AI agent",
		Long: `Interact with the local OpenSVC AI agent.

The ask command submits one non-persistent prompt. The list, show, and delete
commands manage persistent conversations owned by the authenticated OpenSVC
identity. The show command returns conversation metadata only; conversation
messages are not exposed by the agent API. Conversations expire automatically.

The agent is local to the node. OPENSVC_AI_AGENT_URL can override its default
loopback URL for local development or non-default local deployments.`,
		Example: `  om ai ask "Assess the health of my cluster"
  om ai list
  om ai list --output json
  om ai show CONVERSATION_ID
  om ai delete CONVERSATION_ID`,
	}
	cmd.AddCommand(
		newCmdAIAsk(),
		newCmdAIList(),
		newCmdAIShow(),
		newCmdAIDelete(),
	)
	return cmd
}

func newCmdAIAsk() *cobra.Command {
	options := omcmd.CmdAIAsk{
		Timeout: omcmd.DefaultAIAskTimeout,
	}
	cmd := &cobra.Command{
		Use:   "ask PROMPT",
		Short: "submit one prompt to the local OpenSVC AI agent",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Prompt = strings.Join(args, " ")
			options.Out = cmd.OutOrStdout()
			options.ErrOut = cmd.ErrOrStderr()
			return runAICommand(cmd, options.Run)
		},
	}
	flags := cmd.Flags()
	flags.DurationVar(&options.Timeout, "timeout", omcmd.DefaultAIAskTimeout, "maximum duration for the complete AI request")
	return cmd
}

func newCmdAIList() *cobra.Command {
	options := omcmd.CmdAIList{OptsAIConversation: omcmd.OptsAIConversation{
		Timeout: omcmd.DefaultAIConversationTimeout,
	}}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "list persistent AI conversations",
		Long:    "List active persistent conversations owned by the authenticated OpenSVC identity, ordered by most recent update.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.Out = cmd.OutOrStdout()
			return runAICommand(cmd, options.Run)
		},
	}
	flags := cmd.Flags()
	flags.DurationVar(&options.Timeout, "timeout", omcmd.DefaultAIConversationTimeout, "maximum duration for the conversation request")
	commoncmd.FlagOutput(flags, &options.Output)
	commoncmd.FlagColor(flags, &options.Color)
	return cmd
}

func newCmdAIShow() *cobra.Command {
	options := omcmd.CmdAIShow{OptsAIConversation: omcmd.OptsAIConversation{
		Timeout: omcmd.DefaultAIConversationTimeout,
	}}
	cmd := &cobra.Command{
		Use:   "show CONVERSATION_ID",
		Short: "show persistent AI conversation metadata",
		Long:  "Show metadata for one owned persistent conversation. Stored messages and model output are not exposed.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.ID = args[0]
			options.Out = cmd.OutOrStdout()
			return runAICommand(cmd, options.Run)
		},
	}
	flags := cmd.Flags()
	flags.DurationVar(&options.Timeout, "timeout", omcmd.DefaultAIConversationTimeout, "maximum duration for the conversation request")
	commoncmd.FlagOutput(flags, &options.Output)
	commoncmd.FlagColor(flags, &options.Color)
	return cmd
}

func newCmdAIDelete() *cobra.Command {
	options := omcmd.CmdAIDelete{OptsAIConversation: omcmd.OptsAIConversation{
		Timeout: omcmd.DefaultAIConversationTimeout,
	}}
	cmd := &cobra.Command{
		Use:     "delete CONVERSATION_ID",
		Aliases: []string{"del"},
		Short:   "delete a persistent AI conversation",
		Long:    "Delete one owned persistent conversation. Successful deletion produces no output.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.ID = args[0]
			return runAICommand(cmd, options.Run)
		},
	}
	cmd.Flags().DurationVar(&options.Timeout, "timeout", omcmd.DefaultAIConversationTimeout, "maximum duration for the conversation request")
	return cmd
}

func runAICommand(cmd *cobra.Command, run func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}
