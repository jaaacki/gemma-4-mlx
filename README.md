# vLLM Metal evaluation rig

Single-Mac inference endpoint test bed: vLLM on Apple Silicon (Metal/MLX) serving OpenAI-compatible HTTP, evaluated as a candidate path for limited-capacity production serving.

For project context, current verified state, and known limits, read `HANDOFF-*.md` (the most recent file). `CLAUDE.md` is the project's working agreement for Claude Code.

## Architecture

Four layers, designed so each can be replaced independently:

```
clients  ─►  edge (nginx, optional)  ─►  engine (vllm-metal)  ◄─  operator (scripts; forge in progress)
                                                                       │
                                                                       ▼
                                                                 state/, profiles/
```

- **Edge** (`deploy/nginx/`): nginx in Docker, public listener, terminates client traffic. Forwards to engine on localhost. Optional; if no external clients, can stay off.
- **Engine** (`vllm-metal` Python subprocess): single model loaded, OpenAI-compatible HTTP on `127.0.0.1:8000`, weights from `~/.cache/huggingface/`.
- **Operator** (`scripts/*.sh` today, `cmd/forge/` Go binary later): spawns the engine, manages lifecycle, writes status to `state/`.
- **Bench** (`bench/`): synthetic load harness; talks the same OpenAI HTTP as any client.

## Layout

```
scripts/         shell-based operator (until Go binary lands)
  start_engine.sh, stop_engine.sh, status_engine.sh
  use_gemma4.sh, use_qwen36.sh   per-model wrappers w/ correct parsers
  edge_up.sh, edge_down.sh        nginx container lifecycle

deploy/          deployment-time artifacts
  nginx/         Dockerfile, docker-compose.yml, nginx.conf
  profiles/      (future) TOML per-model profiles

bench/           Python load-test harness
  harness.py     async OpenAI client, TTFT + throughput + RSS sampling
  results/       gitignored

state/           runtime artifacts written by the engine
  vllm-metal.pid, vllm-metal.log (gitignored)

.venv-vllm-metal/   engine venv (gitignored). do not delete.
```

## Common operations

Boot a model (handles parsers + prefix-cache flags):

```bash
./scripts/use_qwen36.sh        # Qwen 3.6 35B-A3B 4-bit, long context, slower TTFT
./scripts/use_gemma4.sh        # Gemma 4 26B-A4B 4-bit, faster TTFT, weaker agent behaviour
```

Status / stop:

```bash
./scripts/status_engine.sh
./scripts/stop_engine.sh
```

Edge (nginx) for external clients:

```bash
./scripts/edge_up.sh           # forwards :18080 → host:8000
./scripts/edge_down.sh
HOST_PORT=19090 ./scripts/edge_up.sh   # different listen port
```

Smoke test the engine directly:

```bash
curl -fsS http://127.0.0.1:8000/v1/models | python3 -m json.tool
```

## Benchmark

```bash
source .venv-vllm-metal/bin/activate    # while uv migration is pending
python -m bench.harness \
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

- **Engine starts/stops via `scripts/*.sh`**, which compute project root via `git rev-parse --show-toplevel`. Run them from anywhere inside the repo.
- **One model at a time.** vLLM serves a single model per process. Model swap = stop + start (~30–80 s including Metal kernel warmup).
- **State is on disk.** PID, log, status flow through `state/`. Bench results through `bench/results/`. Both gitignored.
- **No tests, no lint, no build.** This is an ops harness, not a package.

## In flight

- `cmd/forge/` (Go) will replace `scripts/use_*.sh` with profile-driven `forge boot <profile>`, supervised by launchd.
- `bench/` will move to `uv` so the harness env stops needing manual `source`.
- nginx access log → SQLite tailer (Phase 4) for real-traffic observability beyond the synthetic bench.
