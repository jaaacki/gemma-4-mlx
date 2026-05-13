# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

Evaluation workspace for vLLM Metal on Apple Silicon — a candidate path for limited-capacity production inference serving on a Mac. The eval is for the owner's company; the rig is being shaped to look like a real production endpoint, not a developer toy.

Current state, validated paths, known limits, and outstanding work are captured in the most recent `HANDOFF-*.md` at the repo root. Read that file first before assuming anything about what's been verified.

## Architecture (four layers, interface-isolated)

```
clients ─► edge (nginx)  ─►  engine (vllm-metal)  ◄─  operator (scripts/, forge later)
                                  │
                                  ▼
                            state/, profiles/, ~/.cache/huggingface/
```

- **Engine**: `vllm` + `vllm_metal` Python subprocess, single model loaded, OpenAI-compatible HTTP on `127.0.0.1:8000`. Cannot be rewritten — vLLM IS Python.
- **Edge**: nginx in Docker (`deploy/nginx/`). External listener, auth/TLS/rate-limit boundary (user-owned config). Forwards to engine on localhost. Optional but the default deployment shape.
- **Operator**: today, shell scripts in `scripts/` that wrap `vllm serve` with the right per-model flags. In progress: `cmd/forge/` Go binary that reads `deploy/profiles/*.toml`, supervises the subprocess, exposes status JSON for the edge's health probe.
- **Bench**: `bench/harness.py` — async OpenAI client, TTFT + throughput + tree-wide RSS sampling, JSONL + summary JSON output.

Layers communicate via interfaces (HTTP, signals, files on disk) — no shared code. Each can be replaced independently.

## Repo layout

```
scripts/         operator shell wrappers (will be replaced by forge binary)
deploy/nginx/    Dockerfile, docker-compose.yml, nginx.conf for the edge
deploy/profiles/ (future) per-model TOML profiles
bench/           Python harness; results land in bench/results/ (gitignored)
state/           runtime artifacts: PID file, engine log, future status JSON (gitignored)
.venv-vllm-metal/   engine venv (gitignored, do not touch)
```

## Common commands

Boot a configured profile (handles parsers, prefix cache, context length):

```bash
./scripts/use_qwen36.sh
./scripts/use_gemma4.sh
```

Lifecycle:

```bash
./scripts/status_engine.sh
./scripts/stop_engine.sh
```

Edge:

```bash
./scripts/edge_up.sh             # nginx on 127.0.0.1:18080 → host:8000
./scripts/edge_down.sh
HOST_PORT=19090 ./scripts/edge_up.sh
```

Lower-level engine boot (bypasses use_*.sh wrappers, for one-offs):

```bash
MAX_MODEL_LEN=8192 ./scripts/start_engine.sh some/model-id
EXTRA_VLLM_ARGS='--enable-prefix-caching --tool-call-parser X' \
  ./scripts/start_engine.sh some/model-id
```

Benchmark (engine must be running; bench is `uv`-managed, separate from the engine venv):

```bash
# one-time: brew install uv && uv sync --project bench
uv run --project bench python -m bench.harness --model <id> --stream --requests 20 --concurrency 4 \
  --max-tokens 128 --jsonl bench/results/<name>.jsonl
```

Pass `--base-url http://127.0.0.1:18080/v1` to bench through the edge instead of the engine direct.

## Conventions that matter

- **Scripts compute project root via `git rev-parse --show-toplevel`.** Safe to run from any subdirectory.
- **`state/` and `bench/results/` are gitignored.** Don't commit JSONL/summary output.
- **One model serves at a time.** Model swap = stop + start, ~30–80s including Metal kernel warmup. No hot-swap in vllm-metal 0.2.0.
- **Per-model parsers required for tool-using clients** (opencode, etc.): each model family has its own tool-call grammar (`gemma4`, `qwen3_xml`) and reasoning markers (`gemma4`, `qwen3`). The `use_*.sh` wrappers bake in the correct combo per model. If you boot via `start_engine.sh` directly, you own setting these via `EXTRA_VLLM_ARGS`.
- **Prefix caching must be explicitly enabled for hybrid-attention models** (Qwen 3.6's DeltaNet+Gated, Gemma 4's heterogeneous heads) — vLLM auto-disables it for these architectures. `use_*.sh` already passes `--enable-prefix-caching`. Without it, every turn pays full prefill.
- **No tests, lint, or build step.** Scripts + harness workspace, not a package. Don't invent CI commands.
- **Evaluation matrix** (per HANDOFF): sweep `concurrency ∈ {1,2,4,8,16}`, `max_model_len ∈ {8192, 32768, 65536, 131072}`, `max_tokens ∈ {128, 512, 1024}`, `prompt_repeat ∈ {1, 50, 200}`, `stream ∈ {false, true}`. Record p50/p95/p99 latency, TTFT, throughput, RSS, macOS swap pressure. If swap grows, mark the config overloaded even if requests complete.

## In flight (do not assume these exist yet)

- `cmd/forge/` (Go) operator binary. Replaces `scripts/use_*.sh` with `forge boot <profile>` reading TOML.
- `deploy/profiles/*.toml` per-model declarative config.
- `deploy/launchd/*.plist` for boot-time supervision.
- `cmd/tailer/` access-log → SQLite observability.
- `bench/pyproject.toml` + `uv` migration to eliminate venv friction.

Stage 1 (this layout) is complete; later stages are sequenced and tracked in the most recent HANDOFF.
