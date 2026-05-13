package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jaaacki/gemma-4-mlx/internal/engine"
	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report engine status",
	Long: `Report whether the engine is running, the PID, model, and endpoint.

With --json, emits a single JSON object on stdout and nothing else. Logs
still go to stderr, so the stdout stream is safe to pipe into jq.`,
	Args: cobra.NoArgs,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "emit pure JSON on stdout (no human formatting)")
}

func runStatus(cmd *cobra.Command, args []string) error {
	eng := engine.New(rootOpts.root)
	st, err := eng.Status()
	if err != nil {
		return fmt.Errorf("reading engine status: %w", err)
	}

	if statusJSON {
		// Pure JSON to stdout. No log noise contaminates this stream.
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(st); err != nil {
			return fmt.Errorf("encoding status JSON: %w", err)
		}
		return nil
	}

	// Human format on stdout (so it composes with shell pipelines too).
	fmt.Fprintf(os.Stdout, "running:   %t\n", st.Running)
	if st.Running {
		fmt.Fprintf(os.Stdout, "pid:       %d\n", st.PID)
	}
	if st.Model != "" {
		fmt.Fprintf(os.Stdout, "model:     %s\n", st.Model)
	}
	if st.Endpoint != "" {
		fmt.Fprintf(os.Stdout, "endpoint:  %s\n", st.Endpoint)
	}
	if st.BootTime != nil && !st.BootTime.IsZero() {
		fmt.Fprintf(os.Stdout, "boot_time: %s\n", st.BootTime.Format("2006-01-02T15:04:05Z07:00"))
	}
	fmt.Fprintf(os.Stdout, "pid_file:  %s\n", st.PIDFile)
	fmt.Fprintf(os.Stdout, "log_file:  %s\n", st.LogFile)
	if st.LastError != "" {
		fmt.Fprintf(os.Stdout, "last_err:  %s\n", st.LastError)
	}
	return nil
}
