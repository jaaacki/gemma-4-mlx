# vLLM Metal Apple Silicon evaluation

This workspace is for testing experimental vLLM-style edge serving on Apple Silicon before deciding whether the path is viable for Gemma 4 production work.

Current verified state:

- vLLM Metal boots on this machine.
- MLX sees the Apple Silicon GPU.
- The OpenAI-compatible API works with the default smoke-test model.
- Gemma 4, MTP/speculative decoding, and KV-cache compression are not yet validated here.

## Machine observed during setup

```text
arch: arm64
macOS: 26.4.1
python used: 3.12.13
```

## Installed local environment

```bash
source .venv-vllm-metal/bin/activate
python - <<'PY'
import mlx.core as mx
import vllm
import vllm_metal
print('vllm', vllm.__version__)
print('mlx device', mx.default_device())
print('vllm_metal imported')
PY
```

Observed versions:

```text
vLLM: 0.20.1
vLLM Metal: 0.2.0
MLX: 0.31.2
```

The vLLM Metal wheel installed during setup was:

```text
https://github.com/vllm-project/vllm-metal/releases/download/v0.2.0-20260509-055449/vllm_metal-0.2.0-cp312-cp312-macosx_11_0_arm64.whl
```

The base vLLM source version built by the upstream installer was `v0.20.1`.

## Server lifecycle

Start the default vLLM Metal smoke-test server:

```bash
./start_vllm_metal.sh
```

Start a specific model:

```bash
MAX_MODEL_LEN=8192 ./start_vllm_metal.sh Qwen/Qwen3-0.6B
```

Start with experimental paged attention explicitly enabled:

```bash
VLLM_METAL_USE_PAGED_ATTENTION=1 \
MAX_MODEL_LEN=8192 \
./start_vllm_metal.sh Qwen/Qwen3-0.6B
```

Pass extra vLLM flags:

```bash
EXTRA_VLLM_ARGS='--trust-remote-code' \
./start_vllm_metal.sh some/model-id
```

Pass speculative config if the installed vLLM Metal build supports it for the selected model:

```bash
SPECULATIVE_CONFIG='{"method":"mtp","num_speculative_tokens":1}' \
./start_vllm_metal.sh some/gemma-4-model
```

Check status:

```bash
./status_vllm_metal.sh
```

Stop:

```bash
./stop_vllm_metal.sh
```

Logs and PID files live under `logs/` and are ignored by git.

## Docker API proxy

Docker Desktop on macOS does not expose Apple Metal/MLX GPU execution inside Linux containers. The vLLM Metal inference server must stay native on the host. The container in this repo is therefore an API proxy: it exposes a 5-digit host port and forwards to the native vLLM Metal server.

Start native vLLM Metal first:

```bash
./start_vllm_metal.sh
```

Start the Docker proxy on `127.0.0.1:18080`:

```bash
./start_proxy_container.sh
```

Use a different 5-digit host port:

```bash
HOST_PORT=19090 ./start_proxy_container.sh
```

Stop the proxy:

```bash
./stop_proxy_container.sh
```

## Smoke test

Native endpoint:

```bash
curl -fsS http://127.0.0.1:8000/v1/models | python3 -m json.tool
```

Docker proxy endpoint:

```bash
curl -fsS http://127.0.0.1:18080/v1/models | python3 -m json.tool
```

```bash
curl -fsS http://127.0.0.1:18080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "Qwen/Qwen3-0.6B",
    "messages": [{"role":"user","content":"Say hello."}],
    "max_tokens": 64
  }' | python3 -m json.tool
```

## Benchmark

Non-streaming:

```bash
source .venv-vllm-metal/bin/activate
python bench_openai.py \
  --model Qwen/Qwen3-0.6B \
  --requests 20 \
  --warmup 2 \
  --concurrency 4 \
  --max-tokens 128 \
  --jsonl results/qwen-smoke-c4.jsonl
```

Streaming TTFT test:

```bash
source .venv-vllm-metal/bin/activate
python bench_openai.py \
  --model Qwen/Qwen3-0.6B \
  --stream \
  --requests 20 \
  --warmup 2 \
  --concurrency 4 \
  --max-tokens 128 \
  --jsonl results/qwen-stream-c4.jsonl
```

Use `--prompt-repeat` to create larger prefill workloads:

```bash
python bench_openai.py \
  --model Qwen/Qwen3-0.6B \
  --stream \
  --requests 20 \
  --concurrency 4 \
  --prompt-repeat 200 \
  --max-tokens 128
```

## Evaluation matrix

Run this for each candidate model that boots:

```text
concurrency: 1, 2, 4, 8, 16
max_model_len: 8192, 32768, 65536 if memory allows
max_tokens: 128, 512, 1024
prompt_repeat: 1, 50, 200
stream: false, true
```

Record:

- boot success/failure
- model load time from `logs/vllm-metal.log`
- p50/p95/p99 latency
- streaming TTFT
- chars/sec or output tokens/sec
- failures/timeouts
- process RSS before/after
- macOS memory pressure and swap from Activity Monitor

If macOS swaps, treat that config as overloaded even if requests complete.

## Gemma 4 next step

Do not claim Gemma 4 viability until exact model IDs have been tested. Candidate IDs should be written down only after they boot successfully in this workspace.

The next concrete test should be:

1. Identify MLX-compatible Gemma 4 model IDs.
2. Boot the smallest Gemma 4 candidate with `MAX_MODEL_LEN=8192`.
3. Run smoke, non-streaming benchmark, streaming benchmark.
4. Repeat with `VLLM_METAL_USE_PAGED_ATTENTION=1`.
5. Try `SPECULATIVE_CONFIG` only after a normal Gemma 4 boot succeeds.
