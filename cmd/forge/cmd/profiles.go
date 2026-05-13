package cmd

import (
	"fmt"
	"os"

	"github.com/jaaacki/gemma-4-mlx/internal/profile"
	"github.com/spf13/cobra"
)

var profilesCmd = &cobra.Command{
	Use:   "profiles",
	Short: "List available profiles",
	Long: `List the names of all *.toml profiles under deploy/profiles/.
Names are printed one per line on stdout, sans .toml suffix.`,
	Args: cobra.NoArgs,
	RunE: runProfiles,
}

func runProfiles(cmd *cobra.Command, args []string) error {
	dir := profilesDir()
	names, err := profile.List(dir)
	if err != nil {
		return fmt.Errorf("listing profiles in %s: %w", dir, err)
	}
	for _, n := range names {
		fmt.Fprintln(os.Stdout, n)
	}
	return nil
}
