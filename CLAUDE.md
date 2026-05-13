# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

Evaluation workspace for vLLM Metal on Apple Silicon. The goal is to decide whether this stack is viable for Gemma 4 edge serving. Treat results as provisional — only the OpenAI-compatible API with a Qwen smoke-test model has been verified. Gemma 4, MTP/speculative decoding, paged attention, and KV-cache compression are **not yet validated** here, so do not assert they work without booting a fresh test in this workspace.

## Architecture

Two-process layout, deliberate and load-bearing:

1. **Native vLLM Metal server on the host** (`127.0.0.1:8000`). Must run natively because Docker Desktop on macOS cannot expose Metal/MLX GPU to Linux containers. Lives in `.venv-vllm-metal/` (Python 3.12, vLLM 0.20.1, vllm_metal 0.2.0, MLX 0.31.2). Managed via `start_vllm_metal.sh` / `stop_vllm_metal.sh` / `status_vllm_metal.sh`, which write `logs/vllm-metal.pid` and `logs/vllm-metal.log`.
2. **Docker nginx proxy** (`127.0.0.1:18080` → `host.docker.internal:8000`). Exists only so callers get a containerized 5-digit endpoint; it does no inference. Defined by `Dockerfile` + `nginx.conf` + `docker-compose.yml` and managed via `start_proxy_container.sh` / `stop_proxy_container.sh`.

`bench_openai.py` is the evaluation harness — async OpenAI client, optional streaming for TTFT, samples server RSS via the PID file from `start_vllm_metal.sh`, and writes per-request JSONL plus a summary JSON with p50/p95/p99 latency and throughput.

## Common commands

Start / stop / status of the native server (env vars override defaults):

```bash
./start_vllm_metal.sh                                 # default smoke model (Qwen/Qwen3-0.6B)
MAX_MODEL_LEN=8192 ./start_vllm_metal.sh some/model   # positional arg or MODEL= overrides model
VLLM_METAL_USE_PAGED_ATTENTION=1 ./start_vllm_metal.sh some/model
SPECULATIVE_CONFIG='{"method":"mtp","num_speculative_tokens":1}' ./start_vllm_metal.sh some/model
EXTRA_VLLM_ARGS='--trust-remote-code' ./start_vllm_metal.sh some/model
./status_vllm_metal.sh
./stop_vllm_metal.sh
```

Docker proxy (start the native server first):

```bash
./start_proxy_container.sh                # exposes 127.0.0.1:18080
HOST_PORT=19090 ./start_proxy_container.sh
./stop_proxy_container.sh
```

Benchmark (always activate the venv first; this is the same env that runs the server, but the client just needs `openai` + `psutil`):

```bash
source .venv-vllm-metal/bin/activate
python bench_openai.py --model Qwen/Qwen3-0.6B --requests 20 --warmup 2 --concurrency 4 --max-tokens 128 \
  --jsonl results/qwen-smoke-c4.jsonl
python bench_openai.py --model Qwen/Qwen3-0.6B --stream --requests 20 --concurrency 4 --max-tokens 128
python bench_openai.py --model Qwen/Qwen3-0.6B --stream --concurrency 4 --prompt-repeat 200  # larger prefill
```

Pass `--base-url http://127.0.0.1:18080/v1` to benchmark through the Docker proxy instead of the native port.

## Conventions that matter

- **PID/log files in `logs/`** are how the scripts coordinate. `start_vllm_metal.sh` refuses to start if `logs/vllm-metal.pid` points to a live process. `bench_openai.py --pid-file` defaults to the same file so it can record server RSS deltas.
- **No tests, lint, or build step.** This is a scripts + harness workspace, not a package. Don't invent CI commands.
- **`results/` and `logs/`** are gitignored. JSONL benchmark output is meant to be regenerated, not committed.
- **Evaluation matrix** (from README): sweep `concurrency ∈ {1,2,4,8,16}`, `max_model_len ∈ {8192, 32768, 65536}`, `max_tokens ∈ {128, 512, 1024}`, `prompt_repeat ∈ {1, 50, 200}`, `stream ∈ {false, true}`. Record boot success, load time from the log, latency percentiles, TTFT, throughput, RSS, and macOS memory pressure. If macOS swaps, mark the config overloaded even if requests complete.
- **Gemma 4 status**: not yet booted here. The README spells out the intended sequence — find MLX-compatible Gemma 4 IDs, boot smallest at `MAX_MODEL_LEN=8192`, smoke + bench, then retry with paged attention, and only attempt `SPECULATIVE_CONFIG` after a clean Gemma 4 boot.
