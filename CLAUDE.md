# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

Evaluation workspace for vLLM Metal on Apple Silicon — a candidate path for limited-capacity production inference serving on a Mac. The eval is for the owner's company; the rig is being shaped to look like a real production endpoint, not a developer toy.

Current state, validated paths, known limits, and outstanding work are captured in the most recent `HANDOFF-*.md` at the repo root. Read that file first before assuming anything about what's been verified.

## Architecture (four layers, interface-isolated)

```
clients ─► edge (nginx)  ─►  engine (vllm-metal)  ◄─  operator (forge)
                                  │
                                  ▼
                            state/, profiles/, ~/.cache/huggingface/
```

- **Engine**: `vllm` + `vllm_metal` Python subprocess, single model loaded, OpenAI-compatible HTTP on `127.0.0.1:8000`. Cannot be rewritten — vLLM IS Python.
- **Edge**: nginx in Docker (`deploy/nginx/`). External listener, auth/TLS/rate-limit boundary (user-owned config). Forwards to engine on localhost. Optional but the default deployment shape.
- **Operator**: `cmd/forge/` Go binary — shipped. Reads `deploy/profiles/*.toml`, supervises the subprocess via PID file + status JSON in `state/`, persists changes atomically (tmp+rename). The shell scripts under `scripts/` remain as a legacy boot path; `forge stop` is compatible with engines booted via either path.
- **Bench**: `bench/harness.py` — async OpenAI client, TTFT + throughput + tree-wide RSS sampling, JSONL + summary JSON output. Packaged as `gemma-4-bench` from the repo-root `pyproject.toml`.
- **Tailer**: `cmd/tailer/` Go binary — shipped. Follows nginx's JSON access log and persists per-request facts to `state/metrics.sqlite` for real-traffic observability (separate from the synthetic bench).

Layers communicate via interfaces (HTTP, signals, files on disk) — no shared code. Each can be replaced independently.

## Repo layout

```
cmd/forge/        forge operator binary (boot, stop, status, swap, profiles)
cmd/tailer/       access-log → SQLite tailer
internal/         engine lifecycle, profile loader, accesslog parser, store
deploy/nginx/     Dockerfile, docker-compose.yml, nginx.conf for the edge
deploy/profiles/  per-model TOML profiles loaded by forge
deploy/launchd/   com.gemma4.{forge,nginx,tailer}.plist for boot-time supervision
scripts/          legacy shell operator wrappers (retained for compatibility)
bench/            Python harness; results land in bench/results/ (gitignored)
state/            runtime artifacts: PID, log, status JSON, metrics.sqlite (gitignored)
pyproject.toml    repo-root packaging for bench harness (uv-managed)
Makefile          forge build + lifecycle wrappers
.venv-vllm-metal/ engine venv (gitignored, do not touch)
```

## Common commands

`forge` is canonical. The Makefile wraps the common subcommands.

```bash
make build                       # builds bin/forge and bin/tailer
./bin/forge boot qwen36          # or: make boot-qwen
./bin/forge boot gemma4          # or: make boot-gemma
./bin/forge status               # or: make status
./bin/forge stop                 # or: make stop
./bin/forge swap qwen36          # stop + settle + boot, or: make swap-qwen
./bin/forge profiles             # list profiles loaded from deploy/profiles/
```

Edge:

```bash
./scripts/edge_up.sh             # nginx on 127.0.0.1:18080 → host:8000
./scripts/edge_down.sh
HOST_PORT=19090 ./scripts/edge_up.sh
```

Legacy shell path (still works; not deprecated, just demoted to a fallback):

```bash
./scripts/use_qwen36.sh
./scripts/use_gemma4.sh
./scripts/stop_engine.sh
./scripts/status_engine.sh
```

Lower-level engine boot (bypasses use_*.sh wrappers, for one-offs):

```bash
MAX_MODEL_LEN=8192 ./scripts/start_engine.sh some/model-id
EXTRA_VLLM_ARGS='--enable-prefix-caching --tool-call-parser X' \
  ./scripts/start_engine.sh some/model-id
```

Benchmark (engine must be running; bench is uv-managed from the repo root):

```bash
# one-time: brew install uv && uv sync
uv run python -m bench.harness --model <id> --stream --requests 20 --concurrency 4 \
  --max-tokens 128 --jsonl bench/results/<name>.jsonl
```

Pass `--base-url http://127.0.0.1:18080/v1` to bench through the edge instead of the engine direct.

Tests / lint:

```bash
go test ./internal/...           # engine, profile, accesslog, store
go test -race ./internal/...     # race detector (clean as of c0ada95+)
go vet ./...
plutil -lint deploy/launchd/*.plist
```

## Conventions that matter

- **`forge` is the canonical operator; `scripts/` is the legacy path.** Both end up writing the same PID file at `state/vllm-metal.pid` and the same log at `state/vllm-metal.log`. `forge stop` works against engines started by either path — it detects whether the recorded PID is its own process-group leader (forge-spawned) or a child of a shell pgrp (script-spawned) and signals appropriately.
- **PID file is an EXCL lock.** Two concurrent `forge boot` invocations cannot both spawn vllm: `Engine.Start()` uses `O_CREATE|O_EXCL` to claim `state/vllm-metal.pid` with a placeholder "starting" string before forking, then overwrites it with the real PID via tmp+rename once the child is up. Loser gets "already running" or "boot already in progress".
- **State on disk, atomic writes.** PID, status JSON, and the SQLite metrics DB live under `state/`. All file mutations are tmp+rename so concurrent readers never observe half-written content. `state/**` is gitignored except `state/.gitkeep`.
- **`make clean` is safe.** It refuses to wipe state while an engine is running, because doing so would orphan vllm with Metal/MLX memory and port 8000 still held. Use `make stop` first, or `make force-clean` for a true reset.
- **One model serves at a time.** Model swap = `forge swap <profile>` = stop + settle (~2 s) + start (~30–80 s including Metal kernel warmup). No hot-swap in vllm-metal 0.2.0.
- **Per-model parsers required for tool-using clients** (opencode, etc.): each model family has its own tool-call grammar (`gemma4`, `qwen3_xml`) and reasoning markers (`gemma4`, `qwen3`). The profile TOML bakes in the correct combo per model; `forge` passes the right `--tool-call-parser` / `--reasoning-parser` to vllm. Direct shell boot via `start_engine.sh` requires setting these via `EXTRA_VLLM_ARGS`.
- **Prefix caching must be explicit for hybrid-attention models** (Qwen 3.6's DeltaNet+Gated, Gemma 4's heterogeneous heads). vLLM auto-disables it for these architectures; the profile TOMLs pass `--enable-prefix-caching`. Without it, every turn pays full prefill.
- **Evaluation matrix** (per HANDOFF): sweep `concurrency ∈ {1,2,4,8,16}`, `max_model_len ∈ {8192, 32768, 65536, 131072}`, `max_tokens ∈ {128, 512, 1024}`, `prompt_repeat ∈ {1, 50, 200}`, `stream ∈ {false, true}`. Record p50/p95/p99 latency, TTFT, throughput, RSS, macOS swap pressure. If swap grows, mark the config overloaded even if requests complete.

## In flight (not yet shipped)

- `forge supervise` — crash-recovery loop so launchd can supervise forge instead of bare vllm.
- `/healthz` on nginx — surface engine status JSON for upstream liveness probes.
- nginx port templating — drive `HOST_PORT` from each profile's `server.port` rather than the hard-coded 8000 in the edge config.
- `mlx-community/Qwen3.6-27B-8bit` smoke test (dense, full prefix cache, ~28 GB).
- MTP / speculative decoding profile for Qwen 3.6 (drafter heads exist in `vllm_metal`).

Phases 1–4 (four-layer reorg, forge, tailer, uv bench) are shipped. The most recent `HANDOFF-*.md` is the authoritative record of what's verified vs. provisional.
