package cmd

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/jaaacki/gemma-4-mlx/internal/engine"
	"github.com/jaaacki/gemma-4-mlx/internal/profile"
	"github.com/spf13/cobra"
)

// swapSettleDelay matches the 2 s pause baked into scripts/use_*.sh.
const swapSettleDelay = 2 * time.Second

var swapCmd = &cobra.Command{
	Use:   "swap <profile-name>",
	Short: "Stop the engine, then boot the named profile",
	Long: `Swap engine profiles: stop the running engine, wait briefly for
Metal to release, then boot the named profile. vLLM Metal does not support
hot-swap; expect 30–80 s of total downtime.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwap,
}

func runSwap(cmd *cobra.Command, args []string) error {
	name := args[0]
	dir := profilesDir()

	// Load + validate the new profile *before* taking the running engine down.
	// profile.Load already validates; the returned error already wraps
	// validation failures with profile context, so a separate Validate()
	// call would be redundant.
	slog.Info("swap: loading target profile", "name", name)
	p, err := profile.LoadByName(dir, name)
	if err != nil {
		return fmt.Errorf("loading profile %q: %w", name, err)
	}

	eng := engine.New(rootOpts.root)

	if running, pid := eng.IsRunning(); running {
		slog.Info("swap: stopping current engine", "pid", pid)
		if err := eng.Stop(); err != nil {
			return fmt.Errorf("stopping engine before swap: %w", err)
		}
	} else {
		slog.Info("swap: engine not running, proceeding to boot")
	}

	slog.Info("swap: waiting for shutdown to settle", "delay", swapSettleDelay)
	time.Sleep(swapSettleDelay)

	slog.Info("swap: starting engine with new profile", "model", p.ModelID())
	if err := eng.Start(p, nil); err != nil {
		return fmt.Errorf("starting engine for profile %q: %w", name, err)
	}

	st, err := eng.Status()
	if err != nil {
		slog.Warn("swap: started, but status read failed", "err", err)
		return nil
	}
	slog.Info("swap: engine started",
		"pid", st.PID,
		"model", st.Model,
		"endpoint", st.Endpoint,
	)
	return nil
}
