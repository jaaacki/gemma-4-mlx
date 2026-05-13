// Package profile loads and validates TOML profile files that describe a
// vLLM-Metal serving configuration. A Profile is the single source of truth
// for the flags that get passed to `vllm serve` and the environment that
// wraps it. The shell scripts under scripts/use_*.sh are the legacy form of
// the same data; the TOML files under deploy/profiles/ replace them and are
// consumed by the `forge` operator binary.
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Profile is the in-memory representation of a TOML profile file.
type Profile struct {
	Model   ModelConfig   `toml:"model"`
	Server  ServerConfig  `toml:"server"`
	Parsers ParsersConfig `toml:"parsers"`
	Cache   CacheConfig   `toml:"cache"`
	Extra   ExtraConfig   `toml:"extra"`

	// Source is the absolute (or as-given) path the profile was loaded from.
	// It is populated by Load / LoadByName and is not part of the TOML file
	// itself.
	Source string `toml:"-"`
}

// ModelConfig describes the model to serve. ID is the HF/MLX repo identifier
// that vLLM resolves; DisplayName and ArchFamily are informational.
type ModelConfig struct {
	ID          string `toml:"id"`
	DisplayName string `toml:"display_name"`
	ArchFamily  string `toml:"arch_family"`
}

// ServerConfig describes the listen address and context window. MaxModelLen
// maps to vLLM's --max-model-len flag. ServedModelName, when non-empty,
// maps to vLLM's --served-model-name (space-separated tokens). The first
// token becomes the canonical name in responses; the rest are aliases that
// route to the same model — used to make Anthropic clients (Claude Code)
// happy without changing the underlying weights.
type ServerConfig struct {
	Host            string   `toml:"host"`
	Port            int      `toml:"port"`
	MaxModelLen     int      `toml:"max_model_len"`
	ServedModelName []string `toml:"served_model_name"`
}

// ParsersConfig configures the chat-template parsers. Both fields must be
// either set together or omitted together; partial configuration produces
// inconsistent behavior from vLLM.
type ParsersConfig struct {
	ToolCall  string `toml:"tool_call"`
	Reasoning string `toml:"reasoning"`
}

// CacheConfig controls KV-cache features. Today this is just the
// prefix-caching toggle; more knobs (paged attention, KV-compression) will
// land alongside Coder D's work.
type CacheConfig struct {
	PrefixCaching bool `toml:"prefix_caching"`
}

// ExtraConfig is the escape hatch. Flags are passed verbatim to `vllm serve`
// after the synthesized flags; Env is layered on top of the inherited process
// environment when forge launches the engine.
type ExtraConfig struct {
	Flags []string          `toml:"flags"`
	Env   map[string]string `toml:"env"`
}

// Load reads and parses a TOML profile from the given path. The returned
// Profile is validated; callers receive an error if either parsing or
// validation fails. The Source field is populated with the input path.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile: read %s: %w", path, err)
	}

	var p Profile
	if _, err := toml.Decode(string(data), &p); err != nil {
		return nil, fmt.Errorf("profile: decode %s: %w", path, err)
	}
	p.Source = path

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("profile: validate %s: %w", path, err)
	}
	return &p, nil
}

// LoadByName resolves filepath.Join(profilesDir, name+".toml") and delegates
// to Load. It is the convenience entry point for `forge run <name>`.
func LoadByName(profilesDir, name string) (*Profile, error) {
	if name == "" {
		return nil, errors.New("profile: empty name")
	}
	path := filepath.Join(profilesDir, name+".toml")
	return Load(path)
}

// List returns the base names (no extension) of every *.toml file in
// profilesDir, sorted lexicographically. Files that don't end in ".toml" are
// ignored.
func List(profilesDir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(profilesDir, "*.toml"))
	if err != nil {
		return nil, fmt.Errorf("profile: glob %s: %w", profilesDir, err)
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		names = append(names, strings.TrimSuffix(base, ".toml"))
	}
	sort.Strings(names)
	return names, nil
}

// Validate enforces the invariants the rest of forge relies on. It returns
// the first violation it finds, wrapped with enough context to point at the
// offending field.
func (p *Profile) Validate() error {
	if p == nil {
		return errors.New("profile: nil receiver")
	}
	if strings.TrimSpace(p.Model.ID) == "" {
		return errors.New("profile: model.id is required")
	}
	if p.Server.Port < 1 || p.Server.Port > 65535 {
		return fmt.Errorf("profile: server.port %d out of range (1..65535)", p.Server.Port)
	}
	if p.Server.MaxModelLen <= 0 {
		return fmt.Errorf("profile: server.max_model_len must be > 0, got %d", p.Server.MaxModelLen)
	}

	tcEmpty := strings.TrimSpace(p.Parsers.ToolCall) == ""
	rsEmpty := strings.TrimSpace(p.Parsers.Reasoning) == ""
	if tcEmpty != rsEmpty {
		return fmt.Errorf("profile: parsers.tool_call (%q) and parsers.reasoning (%q) must both be set or both be empty",
			p.Parsers.ToolCall, p.Parsers.Reasoning)
	}
	return nil
}

// VLLMArgs renders the full argv (minus argv[0]) for `vllm`. The first token
// is always "serve" followed by the model ID, matching scripts/start_engine.sh.
//
// Synthesized flags (parsers, prefix caching) are emitted first; Extra.Flags
// are appended last so a user can override behavior by repeating a flag —
// vLLM honors the last occurrence. Flags that already appear in Extra.Flags
// are NOT synthesized again to avoid duplicates like
// `--enable-auto-tool-choice --enable-auto-tool-choice`.
func (p *Profile) VLLMArgs() []string {
	extraSet := make(map[string]struct{}, len(p.Extra.Flags))
	for _, f := range p.Extra.Flags {
		// Only the flag token itself matters for dedup; values that follow a
		// flag in the same slice element are left untouched.
		token := f
		if i := strings.IndexAny(f, " ="); i >= 0 {
			token = f[:i]
		}
		extraSet[token] = struct{}{}
	}

	args := []string{
		"serve", p.Model.ID,
		"--host", p.Server.Host,
		"--port", strconv.Itoa(p.Server.Port),
		"--max-model-len", strconv.Itoa(p.Server.MaxModelLen),
	}

	if len(p.Server.ServedModelName) > 0 {
		if _, dup := extraSet["--served-model-name"]; !dup {
			// vLLM's --served-model-name takes space-separated tokens as a
			// single flag with multiple following positional values. Append
			// the flag once, then each alias as its own argv element.
			args = append(args, "--served-model-name")
			args = append(args, p.Server.ServedModelName...)
		}
	}

	if p.Parsers.ToolCall != "" {
		if _, dup := extraSet["--tool-call-parser"]; !dup {
			args = append(args, "--tool-call-parser", p.Parsers.ToolCall)
		}
	}
	if p.Parsers.Reasoning != "" {
		if _, dup := extraSet["--reasoning-parser"]; !dup {
			args = append(args, "--reasoning-parser", p.Parsers.Reasoning)
		}
	}
	if p.Cache.PrefixCaching {
		if _, dup := extraSet["--enable-prefix-caching"]; !dup {
			args = append(args, "--enable-prefix-caching")
		}
	}

	if len(p.Extra.Flags) > 0 {
		args = append(args, p.Extra.Flags...)
	}
	return args
}

// Env returns a copy of the env map declared in [extra.env]. Callers should
// layer this on top of os.Environ() rather than replace it — forge needs the
// inherited PATH/HOME/etc. to find the venv's `vllm` binary.
func (p *Profile) Env() map[string]string {
	if len(p.Extra.Env) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(p.Extra.Env))
	for k, v := range p.Extra.Env {
		out[k] = v
	}
	return out
}

// ModelID is a convenience accessor for log lines and CLI status output.
func (p *Profile) ModelID() string {
	return p.Model.ID
}
