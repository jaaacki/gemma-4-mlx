package cmd

import (
	"fmt"
	"log/slog"

	"github.com/jaaacki/gemma-4-mlx/internal/engine"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running engine",
	Long: `Stop the vLLM Metal engine subprocess. No-op (exit 0) if the engine
is not running.`,
	Args: cobra.NoArgs,
	RunE: runStop,
}

func runStop(cmd *cobra.Command, args []string) error {
	eng := engine.New(rootOpts.root)
	// Always call Stop() — it handles the no-pid-file / stale-pid-file
	// cases internally (returns nil, cleaning up stale files). Skipping
	// the call on IsRunning()==false would leave stale PID files around
	// forever.
	slog.Info("stop: stopping engine")
	if err := eng.Stop(); err != nil {
		return fmt.Errorf("stopping engine: %w", err)
	}
	slog.Info("stop: engine stopped")
	return nil
}
