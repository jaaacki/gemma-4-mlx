# vLLM Metal evaluation rig

Single-Mac inference endpoint test bed: vLLM on Apple Silicon (Metal/MLX) serving OpenAI-compatible HTTP, evaluated as a candidate path for limited-capacity production serving.

For project context, current verified state, and known limits, read `HANDOFF-*.md` (the most recent file). `CLAUDE.md` is the project's working agreement for Claude Code.

## Architecture

Four layers, designed so each can be replaced independently:

```
clients  ─►  edge (nginx, optional)  ─►  engine (vllm-metal)  ◄─  operator (forge)
                                                                       │
                                                                       ▼
                                                                 state/, profiles/
```

- **Edge** (`deploy/nginx/`): nginx in Docker, public listener, terminates client traffic. Forwards to engine on localhost. Optional; if no external clients, can stay off.
- **Engine** (`vllm-metal` Python subprocess): single model loaded, OpenAI-compatible HTTP on `127.0.0.1:8000`, weights from `~/.cache/huggingface/`.
- **Operator** (`cmd/forge/` Go binary): reads `deploy/profiles/*.toml`, supervises the subprocess, persists status to `state/`. The shell scripts under `scripts/` are kept as a legacy boot path but `forge` is the canonical operator.
- **Bench** (`bench/`): synthetic load harness; talks the same OpenAI HTTP as any client. Packaged as `gemma-4-bench` with its `pyproject.toml` at the repo root for clean `uv` workflows.

`forge` is the canonical operator. `make boot-qwen` / `make boot-gemma` are thin Makefile wrappers around `forge boot <profile>` for muscle-memory parity with the older script flow.

## Drive it

One-liner per coding harness. Each script ensures the engine is up with the right profile, then launches the harness — no manual boot, no manual env vars.

| Harness | Command | Requires |
|---|---|---|
| **opencode** (TUI, Claude-Code-like) | `./scripts/launch_opencode.sh` | `npm install -g opencode-ai` |
| **Claude Code** (Anthropic's official CLI) | `./scripts/launch_claude_code.sh` | `npm install -g @anthropic-ai/claude-code` |
| **aider** (REPL with diffs) | `./scripts/launch_aider.sh` | `pip install aider-install && aider-install` |
| **bench** (synthetic load) | `./scripts/launch_bench.sh` | `brew install uv` |
| **raw HTTP** (OpenAI-compatible) | `curl http://127.0.0.1:8000/v1/chat/completions ...` | nothing |
| **raw HTTP** (Anthropic Messages API) | `curl http://127.0.0.1:8000/v1/messages ...` | nothing |

All four launch scripts accept the same shape:

```bash
./scripts/launch_<harness>.sh [PROFILE] [-- harness-args...]

# Default profile is qwen36 (Qwen 3.6 35B-A3B 4-bit MLX).
./scripts/launch_opencode.sh
./scripts/launch_opencode.sh gemma4
./scripts/launch_opencode.sh qwen36 -- run "explain bench/harness.py"

# Bench accepts the harness's flags after --
./scripts/launch_bench.sh qwen36 -- --requests 50 --concurrency 8 --max-tokens 256
```

Each script delegates engine readiness to `scripts/ensure_engine.sh`:

1. Builds `bin/forge` if missing.
2. If the engine is down → boots the requested profile.
3. If the engine is up with a different model → refuses with a `forge swap` hint (won't surprise an active session).
4. Polls `/v1/models` until HTTP-ready (max 3 min).

You can call `ensure_engine.sh` directly too — it's how CI or external supervisors would drive the rig:

```bash
./scripts/ensure_engine.sh qwen36 && <your-tool-against-:8000>
```

To change which model a harness defaults to, edit the corresponding `deploy/profiles/*.toml` (the launch scripts read the model ID from the TOML, no hardcoding).

## Layout

```
cmd/
  forge/          Go operator binary (boot, stop, status, swap, profiles)
  tailer/         access-log → SQLite tailer for real-traffic observability

internal/
  engine/         vllm subprocess lifecycle (PID file, status JSON, signals)
  profile/        TOML profile loader/validator
  accesslog/      nginx JSON access-log parser
  store/          SQLite store for tailer records

deploy/
  nginx/          Dockerfile, docker-compose.yml, nginx.conf (edge)
  profiles/       per-model TOML profiles loaded by forge
  launchd/        com.gemma4.{forge,nginx,tailer}.plist for boot-time supervision

scripts/          legacy shell operator wrappers (start_engine.sh, use_*.sh, edge_*.sh)
                  retained for callers that haven't moved to forge yet

bench/            Python load-test harness (gemma-4-bench package)
  harness.py      async OpenAI client, TTFT + throughput + RSS sampling
  results/        gitignored

state/            runtime artifacts: PID, log, status, metrics.sqlite (gitignored)
                  launchd/  per-service stdout/stderr from launchd-supervised services
                  .gitkeep  keeps the empty directory tracked

pyproject.toml    repo-root packaging metadata for the bench harness
Makefile          forge build + lifecycle wrappers
go.mod / go.sum   Go module for the operator/tailer

.venv-vllm-metal/ engine venv (gitignored, do not delete)
```

## Common operations

Canonical path is `forge`. The Makefile gives muscle-memory wrappers.

Boot a profile:

```bash
make build                       # builds bin/forge and bin/tailer
./bin/forge boot qwen36          # Qwen 3.6 35B-A3B 4-bit, long context, slower TTFT
./bin/forge boot gemma4          # Gemma 4 26B-A4B 4-bit, faster TTFT, weaker agent behaviour
# muscle-memory aliases:
make boot-qwen
make boot-gemma
```

Lifecycle:

```bash
./bin/forge status               # or: make status
./bin/forge stop                 # or: make stop
./bin/forge swap qwen36          # stop + settle + boot in one shot, or: make swap-qwen
./bin/forge profiles             # list available TOML profiles
```

Edge (nginx) for external clients:

```bash
./scripts/edge_up.sh             # forwards :18080 → host:8000
./scripts/edge_down.sh
HOST_PORT=19090 ./scripts/edge_up.sh
```

Smoke test the engine directly:

```bash
curl -fsS http://127.0.0.1:8000/v1/models | python3 -m json.tool
```

Legacy shell scripts under `scripts/` (`use_qwen36.sh`, `use_gemma4.sh`, `start_engine.sh`, `stop_engine.sh`, `status_engine.sh`) still work for callers that haven't moved to `forge`. `forge stop` is compatible with engines booted via those shell scripts as well as engines it booted itself.

## Benchmark

`./scripts/launch_bench.sh` is the convenience wrapper — it ensures the engine is up, reads the model ID from the profile, and runs the harness with sensible defaults. Use that for most cases.

Manual invocation (when you need to script around the bench yourself):

One-time setup:

```bash
brew install uv                  # or `pipx install uv`
uv sync                          # creates .venv with openai + psutil
```

Then run from the repo root:

```bash
uv run python -m bench.harness \
  --model mlx-community/Qwen3.6-35B-A3B-4bit \
  --stream --requests 20 --warmup 2 --concurrency 4 --max-tokens 128 \
  --jsonl bench/results/qwen-stream-c4.jsonl
```

Default `--pid-file` is `state/vllm-metal.pid`, used to sample server-process RSS (tree-wide).

Evaluation matrix:

```text
concurrency: 1, 2, 4, 8, 16
max_model_len: 8192, 32768, 65536, 131072
max_tokens: 128, 512, 1024
prompt_repeat: 1, 50, 200
stream: false, true
```

If macOS swap usage grows during a run, treat the config as overloaded even if requests complete.

## Conventions

- **`forge` is the canonical operator.** Run from the repo root or use `make boot-*` wrappers. The Go binary resolves paths via `forge --root` (defaults to working dir) so it doesn't need `git rev-parse`.
- **Shell scripts under `scripts/` still work** as a legacy boot path. `forge stop` knows how to terminate engines started by either path.
- **One model at a time.** vLLM serves a single model per process. Model swap = `forge swap <profile>` (~30–80 s including Metal kernel warmup).
- **State is on disk.** PID, log, status JSON, and SQLite metrics flow through `state/`. Bench results through `bench/results/`. All gitignored (`state/**` except `.gitkeep`).
- **`make clean` refuses to wipe state while an engine is running** — it would orphan vllm and leak Metal/MLX memory. Use `make stop` first, or `make force-clean` for a true reset.
- **Tests:** `go test ./internal/...` covers engine lifecycle and the access-log parser. `go test -race` is clean.

## In flight

- **`forge supervise`**: crash-recovery loop so launchd can supervise forge instead of bare vllm.
- **`/healthz` on the edge**: nginx config to surface engine status JSON for liveness probes.
- **nginx port templating**: drive `HOST_PORT` from the profile's `server.port` rather than hard-coded 8000.
- **MTP / speculative decoding for Qwen 3.6**: per HANDOFF outstanding items.
- **Dense Qwen 3.6 27B 8-bit smoke test**: candidate "best of both" for agent loops.

`cmd/forge/`, `cmd/tailer/`, `deploy/profiles/*.toml`, `deploy/launchd/*.plist`, and the root `pyproject.toml` are all shipped. Read the most recent `HANDOFF-*.md` for the validated-vs-provisional status of each.
