package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sampleQwen mirrors deploy/profiles/qwen36.toml so the tests don't depend on
// the on-disk file. Keeping it inline also means the test data lives next to
// the assertions that read it.
const sampleQwen = `
[model]
id = "mlx-community/Qwen3.6-35B-A3B-4bit"
display_name = "Qwen 3.6 35B-A3B 4-bit"
arch_family = "qwen3"

[server]
host = "127.0.0.1"
port = 8000
max_model_len = 131072

[parsers]
tool_call = "qwen3_xml"
reasoning = "qwen3"

[cache]
prefix_caching = true

[extra]
flags = ["--enable-auto-tool-choice"]
`

func writeProfile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name+".toml")
	if err := writeFile(path, body); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// writeFile is split out so the table cases stay tidy.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := writeProfile(t, dir, "qwen36", sampleQwen)

	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Model.ID != "mlx-community/Qwen3.6-35B-A3B-4bit" {
		t.Errorf("Model.ID = %q", p.Model.ID)
	}
	if p.Model.ArchFamily != "qwen3" {
		t.Errorf("Model.ArchFamily = %q", p.Model.ArchFamily)
	}
	if p.Server.Host != "127.0.0.1" || p.Server.Port != 8000 || p.Server.MaxModelLen != 131072 {
		t.Errorf("Server = %+v", p.Server)
	}
	if p.Parsers.ToolCall != "qwen3_xml" || p.Parsers.Reasoning != "qwen3" {
		t.Errorf("Parsers = %+v", p.Parsers)
	}
	if !p.Cache.PrefixCaching {
		t.Errorf("Cache.PrefixCaching = false, want true")
	}
	if !reflect.DeepEqual(p.Extra.Flags, []string{"--enable-auto-tool-choice"}) {
		t.Errorf("Extra.Flags = %v", p.Extra.Flags)
	}
	if p.Source != path {
		t.Errorf("Source = %q want %q", p.Source, path)
	}
	if p.ModelID() != p.Model.ID {
		t.Errorf("ModelID() drift")
	}
}

func TestLoadByName(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "qwen36", sampleQwen)

	p, err := LoadByName(dir, "qwen36")
	if err != nil {
		t.Fatalf("LoadByName: %v", err)
	}
	if p.Model.ID == "" {
		t.Fatal("empty Model.ID after LoadByName")
	}

	if _, err := LoadByName(dir, "nope"); err == nil {
		t.Fatal("expected error for missing profile")
	}
	if _, err := LoadByName(dir, ""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestList_Sorted(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "qwen36", sampleQwen)
	writeProfile(t, dir, "gemma4", sampleQwen) // body doesn't matter for List
	writeProfile(t, dir, "alpha", sampleQwen)
	// A non-toml file must be ignored.
	if err := writeFile(filepath.Join(dir, "README.md"), "ignore me"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "gemma4", "qwen36"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
}

func TestValidate(t *testing.T) {
	base := func() *Profile {
		return &Profile{
			Model:   ModelConfig{ID: "x/y"},
			Server:  ServerConfig{Host: "127.0.0.1", Port: 8000, MaxModelLen: 4096},
			Parsers: ParsersConfig{ToolCall: "a", Reasoning: "b"},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*Profile)
		wantErr string // substring, "" means must succeed
	}{
		{
			name:   "ok",
			mutate: func(p *Profile) {},
		},
		{
			name:    "empty model id",
			mutate:  func(p *Profile) { p.Model.ID = "  " },
			wantErr: "model.id",
		},
		{
			name:    "port zero",
			mutate:  func(p *Profile) { p.Server.Port = 0 },
			wantErr: "server.port",
		},
		{
			name:    "port too high",
			mutate:  func(p *Profile) { p.Server.Port = 70000 },
			wantErr: "server.port",
		},
		{
			name:    "max_model_len zero",
			mutate:  func(p *Profile) { p.Server.MaxModelLen = 0 },
			wantErr: "max_model_len",
		},
		{
			name:    "half-set parsers (tool_call only)",
			mutate:  func(p *Profile) { p.Parsers.Reasoning = "" },
			wantErr: "parsers",
		},
		{
			name:    "half-set parsers (reasoning only)",
			mutate:  func(p *Profile) { p.Parsers.ToolCall = "" },
			wantErr: "parsers",
		},
		{
			name: "both parsers empty is ok",
			mutate: func(p *Profile) {
				p.Parsers.ToolCall = ""
				p.Parsers.Reasoning = ""
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.mutate(p)
			err := p.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestVLLMArgs(t *testing.T) {
	cases := []struct {
		name string
		p    *Profile
		want []string
	}{
		{
			name: "qwen36 style with prefix cache and auto-tool-choice in extra",
			p: &Profile{
				Model:   ModelConfig{ID: "mlx-community/Qwen3.6-35B-A3B-4bit"},
				Server:  ServerConfig{Host: "127.0.0.1", Port: 8000, MaxModelLen: 131072},
				Parsers: ParsersConfig{ToolCall: "qwen3_xml", Reasoning: "qwen3"},
				Cache:   CacheConfig{PrefixCaching: true},
				Extra:   ExtraConfig{Flags: []string{"--enable-auto-tool-choice"}},
			},
			want: []string{
				"serve", "mlx-community/Qwen3.6-35B-A3B-4bit",
				"--host", "127.0.0.1",
				"--port", "8000",
				"--max-model-len", "131072",
				"--tool-call-parser", "qwen3_xml",
				"--reasoning-parser", "qwen3",
				"--enable-prefix-caching",
				"--enable-auto-tool-choice",
			},
		},
		{
			name: "no parsers, no prefix cache, no extra",
			p: &Profile{
				Model:  ModelConfig{ID: "Qwen/Qwen3-0.6B"},
				Server: ServerConfig{Host: "0.0.0.0", Port: 9000, MaxModelLen: 8192},
			},
			want: []string{
				"serve", "Qwen/Qwen3-0.6B",
				"--host", "0.0.0.0",
				"--port", "9000",
				"--max-model-len", "8192",
			},
		},
		{
			name: "extra repeats synthesized flag — dedup wins on Extra",
			p: &Profile{
				Model:   ModelConfig{ID: "m"},
				Server:  ServerConfig{Host: "h", Port: 1, MaxModelLen: 1},
				Parsers: ParsersConfig{ToolCall: "tc", Reasoning: "r"},
				Cache:   CacheConfig{PrefixCaching: true},
				Extra: ExtraConfig{
					Flags: []string{"--enable-prefix-caching", "--tool-call-parser", "override"},
				},
			},
			want: []string{
				"serve", "m",
				"--host", "h",
				"--port", "1",
				"--max-model-len", "1",
				// tool-call-parser synthesis suppressed because Extra has it
				"--reasoning-parser", "r",
				// prefix-caching synthesis suppressed because Extra has it
				"--enable-prefix-caching", "--tool-call-parser", "override",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.VLLMArgs()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("VLLMArgs mismatch\n got: %v\nwant: %v", got, tc.want)
			}
		})
	}
}

func TestEnv(t *testing.T) {
	p := &Profile{Extra: ExtraConfig{Env: map[string]string{"VLLM_METAL_USE_PAGED_ATTENTION": "1"}}}
	got := p.Env()
	if got["VLLM_METAL_USE_PAGED_ATTENTION"] != "1" {
		t.Errorf("Env = %v", got)
	}
	// Caller must get a copy, not the live map.
	got["VLLM_METAL_USE_PAGED_ATTENTION"] = "0"
	if p.Extra.Env["VLLM_METAL_USE_PAGED_ATTENTION"] != "1" {
		t.Errorf("Env() returned the live map; should be a defensive copy")
	}

	empty := (&Profile{}).Env()
	if empty == nil {
		t.Errorf("Env on empty profile returned nil; should be empty map")
	}
}

func TestLoad_BadFiles(t *testing.T) {
	dir := t.TempDir()

	// Malformed TOML.
	bad := filepath.Join(dir, "bad.toml")
	if err := writeFile(bad, "this is = = not toml"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(bad); err == nil {
		t.Error("expected decode error")
	}

	// Valid TOML, failing validation.
	invalid := filepath.Join(dir, "invalid.toml")
	body := `
[model]
id = ""
[server]
host = "127.0.0.1"
port = 8000
max_model_len = 8192
`
	if err := writeFile(invalid, body); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(invalid); err == nil {
		t.Error("expected validation error")
	}

	// Missing file.
	if _, err := Load(filepath.Join(dir, "ghost.toml")); err == nil {
		t.Error("expected read error")
	}
}
