// Command forge is the operator-layer CLI for the vLLM Metal evaluation rig.
//
// It wraps the engine subprocess lifecycle (boot / stop / status / swap) and
// resolves model profiles from deploy/profiles/. It replaces the ad-hoc
// scripts/use_*.sh + scripts/{start,stop,status}_engine.sh wrappers.
package main

import (
	"fmt"
	"os"

	"github.com/jaaacki/gemma-4-mlx/cmd/forge/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		// cobra has already printed the error to stderr; just propagate exit code.
		fmt.Fprintln(os.Stderr, "forge: error:", err)
		os.Exit(1)
	}
}
