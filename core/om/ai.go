package om

import (
	"os"
	"os/signal"
	"strings"
	"syscall"

	clientai "github.com/opensvc/om3/v3/core/client/ai"
	"github.com/opensvc/om3/v3/core/omcmd"
	"github.com/spf13/cobra"
)

func init() {
	cmdAI := &cobra.Command{
		Use:   "ai",
		Short: "interact with the local OpenSVC AI agent",
	}
	cmdAI.AddCommand(newCmdAIAsk())
	root.AddCommand(cmdAI)
}

func newCmdAIAsk() *cobra.Command {
	options := omcmd.CmdAIAsk{
		AgentURL: clientai.DefaultEndpoint,
		Timeout:  omcmd.DefaultAIAskTimeout,
	}
	cmd := &cobra.Command{
		Use:   "ask PROMPT",
		Short: "submit one prompt to the local OpenSVC AI agent",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Prompt = strings.Join(args, " ")
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return options.Run(ctx)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.AgentURL, "agent-url", clientai.DefaultEndpoint, "opensvc ai agent ask endpoint")
	flags.DurationVar(&options.Timeout, "timeout", omcmd.DefaultAIAskTimeout, "maximum duration for the complete AI request")
	return cmd
}
