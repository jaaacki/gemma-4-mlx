// Package cmd defines the cobra subcommand tree for the forge CLI.
package cmd

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// rootFlags captures global flags shared by every subcommand.
type rootFlags struct {
	root string // absolute path to the repo root
}

var rootOpts rootFlags

// rootCmd is the top-level cobra command.
var rootCmd = &cobra.Command{
	Use:           "forge",
	Short:         "Operator CLI for the vLLM Metal evaluation rig",
	Long:          "forge manages the local vLLM Metal engine process: boot a profile, stop, swap, and report status.",
	SilenceUsage:  true, // don't dump usage on every runtime error
	SilenceErrors: true, // we print our own
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Resolve --root to an absolute path; fall back to `git rev-parse` if empty.
		if rootOpts.root == "" {
			r, err := detectRepoRoot()
			if err != nil {
				return fmt.Errorf("resolving repo root (pass --root to override): %w", err)
			}
			rootOpts.root = r
		}
		abs, err := filepath.Abs(rootOpts.root)
		if err != nil {
			return fmt.Errorf("resolving --root %q: %w", rootOpts.root, err)
		}
		rootOpts.root = abs
		return nil
	},
}

// Execute runs the root command. Returns the cobra error so main() can set exit code.
func Execute() error {
	// Default logger: slog to stderr. Subcommands that need stdout-only output
	// (e.g. `status --json`) keep using slog because slog goes to stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&rootOpts.root, "root", "", "repo root (default: `git rev-parse --show-toplevel`)")

	rootCmd.AddCommand(bootCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(swapCmd)
	rootCmd.AddCommand(profilesCmd)
}

// detectRepoRoot shells out to `git rev-parse --show-toplevel`.
func detectRepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// profilesDir is the canonical location for *.toml profiles.
func profilesDir() string {
	return filepath.Join(rootOpts.root, "deploy", "profiles")
}
