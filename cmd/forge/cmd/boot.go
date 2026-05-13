package cmd

import (
	"fmt"
	"log/slog"

	"github.com/jaaacki/gemma-4-mlx/internal/engine"
	"github.com/jaaacki/gemma-4-mlx/internal/profile"
	"github.com/spf13/cobra"
)

var bootCmd = &cobra.Command{
	Use:   "boot <profile-name>",
	Short: "Boot the engine with the named profile",
	Long: `Boot the vLLM Metal engine using a TOML profile under deploy/profiles/.

The profile name is the basename without .toml, e.g. "qwen36" loads
deploy/profiles/qwen36.toml. Returns non-zero if the engine is already
running or the profile fails validation.`,
	Args: cobra.ExactArgs(1),
	RunE: runBoot,
}

func runBoot(cmd *cobra.Command, args []string) error {
	name := args[0]
	dir := profilesDir()

	slog.Info("boot: loading profile", "name", name, "profiles_dir", dir)
	p, err := profile.LoadByName(dir, name)
	if err != nil {
		// profile.Load already validates; the returned error already wraps
		// validation failures with profile context.
		return fmt.Errorf("loading profile %q: %w", name, err)
	}

	eng := engine.New(rootOpts.root)
	if running, pid := eng.IsRunning(); running {
		return fmt.Errorf("engine already running (pid=%d); run `forge stop` first", pid)
	}

	slog.Info("boot: starting engine", "model", p.ModelID())
	if err := eng.Start(p, nil); err != nil {
		return fmt.Errorf("starting engine for profile %q: %w", name, err)
	}

	st, err := eng.Status()
	if err != nil {
		// Engine started but status read failed — surface but don't fail.
		slog.Warn("boot: started, but status read failed", "err", err)
		return nil
	}
	slog.Info("boot: engine started",
		"pid", st.PID,
		"model", st.Model,
		"endpoint", st.Endpoint,
		"log_file", st.LogFile,
	)
	return nil
}
