#!/usr/bin/env python3
import argparse
import asyncio
import json
import os
import statistics
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

import psutil
from openai import AsyncOpenAI


@dataclass
class Result:
    index: int
    ok: bool
    latency_s: float
    ttft_s: float | None
    output_tokens: int
    chars: int
    error: str | None = None


def percentile(values: list[float], percentile_value: float) -> float | None:
    if not values:
        return None
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    rank = (len(ordered) - 1) * (percentile_value / 100)
    lower = int(rank)
    upper = min(lower + 1, len(ordered) - 1)
    weight = rank - lower
    return ordered[lower] * (1 - weight) + ordered[upper] * weight


def process_sample(pid_file: str | None) -> dict[str, Any] | None:
    if not pid_file:
        return None
    path = Path(pid_file)
    if not path.exists():
        return None
    try:
        parent = psutil.Process(int(path.read_text().strip()))
        family = [parent, *parent.children(recursive=True)]
        members: list[dict[str, Any]] = []
        rss_total = 0
        for proc in family:
            try:
                rss = proc.memory_info().rss
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                continue
            rss_total += rss
            members.append({"pid": proc.pid, "name": proc.name(), "rss_bytes": rss})
        return {
            "pid": parent.pid,
            "rss_bytes": rss_total,
            "parent_rss_bytes": parent.memory_info().rss,
            "child_count": len(members) - 1,
            "status": parent.status(),
            "members": members,
        }
    except Exception as exc:
        return {"error": repr(exc)}


async def run_non_streaming(
    client: AsyncOpenAI,
    index: int,
    model: str,
    prompt: str,
    max_tokens: int,
    temperature: float,
) -> Result:
    start = time.perf_counter()
    try:
        response = await client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            max_tokens=max_tokens,
            temperature=temperature,
        )
        latency = time.perf_counter() - start
        content = response.choices[0].message.content or ""
        usage = response.usage
        output_tokens = usage.completion_tokens if usage else 0
        return Result(index=index, ok=True, latency_s=latency, ttft_s=None, output_tokens=output_tokens, chars=len(content))
    except Exception as exc:
        return Result(index=index, ok=False, latency_s=time.perf_counter() - start, ttft_s=None, output_tokens=0, chars=0, error=repr(exc))


async def run_streaming(
    client: AsyncOpenAI,
    index: int,
    model: str,
    prompt: str,
    max_tokens: int,
    temperature: float,
) -> Result:
    start = time.perf_counter()
    ttft = None
    chars = 0
    chunks = 0
    try:
        stream = await client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            max_tokens=max_tokens,
            temperature=temperature,
            stream=True,
        )
        async for chunk in stream:
            delta = chunk.choices[0].delta.content if chunk.choices else None
            if delta:
                if ttft is None:
                    ttft = time.perf_counter() - start
                chars += len(delta)
                chunks += 1
        latency = time.perf_counter() - start
        return Result(index=index, ok=True, latency_s=latency, ttft_s=ttft, output_tokens=0, chars=chars)
    except Exception as exc:
        return Result(index=index, ok=False, latency_s=time.perf_counter() - start, ttft_s=ttft, output_tokens=0, chars=chars, error=repr(exc))


async def run_batch(args: argparse.Namespace, measured: bool) -> list[Result]:
    client = AsyncOpenAI(base_url=args.base_url, api_key=args.api_key, timeout=args.timeout)
    prompt = "\n".join([args.prompt] * args.prompt_repeat)
    semaphore = asyncio.Semaphore(args.concurrency)
    count = args.requests if measured else args.warmup

    async def guarded(index: int) -> Result:
        async with semaphore:
            if args.stream:
                return await run_streaming(client, index, args.model, prompt, args.max_tokens, args.temperature)
            return await run_non_streaming(client, index, args.model, prompt, args.max_tokens, args.temperature)

    return await asyncio.gather(*(guarded(i) for i in range(count)))


def summarize(args: argparse.Namespace, results: list[Result], wall_s: float, before: dict[str, Any] | None, after: dict[str, Any] | None) -> dict[str, Any]:
    successes = [r for r in results if r.ok]
    failures = [r for r in results if not r.ok]
    latencies = [r.latency_s for r in successes]
    ttfts = [r.ttft_s for r in successes if r.ttft_s is not None]
    total_output_tokens = sum(r.output_tokens for r in successes)
    total_chars = sum(r.chars for r in successes)

    throughput: dict[str, float] = {"chars_per_s": total_chars / wall_s if wall_s else 0}
    if total_output_tokens:
        throughput["output_tokens_per_s"] = total_output_tokens / wall_s if wall_s else 0

    return {
        "model": args.model,
        "base_url": args.base_url,
        "stream": args.stream,
        "requests": args.requests,
        "warmup": args.warmup,
        "concurrency": args.concurrency,
        "prompt_repeat": args.prompt_repeat,
        "max_tokens": args.max_tokens,
        "successes": len(successes),
        "failures": len(failures),
        "wall_s": wall_s,
        "throughput": throughput,
        "latency_s": {
            "min": min(latencies) if latencies else None,
            "mean": statistics.mean(latencies) if latencies else None,
            "p50": percentile(latencies, 50),
            "p95": percentile(latencies, 95),
            "p99": percentile(latencies, 99),
            "max": max(latencies) if latencies else None,
        },
        "ttft_s": {
            "min": min(ttfts) if ttfts else None,
            "mean": statistics.mean(ttfts) if ttfts else None,
            "p50": percentile(ttfts, 50),
            "p95": percentile(ttfts, 95),
            "p99": percentile(ttfts, 99),
            "max": max(ttfts) if ttfts else None,
        },
        "process_before": before,
        "process_after": after,
        "sample_errors": [r.error for r in failures[:5]],
    }


async def main() -> None:
    parser = argparse.ArgumentParser(description="OpenAI-compatible concurrency benchmark for vLLM-style local servers.")
    parser.add_argument("--base-url", default="http://127.0.0.1:8000/v1")
    parser.add_argument("--api-key", default="local")
    parser.add_argument("--model", required=True)
    parser.add_argument("--requests", type=int, default=20)
    parser.add_argument("--warmup", type=int, default=2)
    parser.add_argument("--concurrency", type=int, default=4)
    parser.add_argument("--max-tokens", type=int, default=128)
    parser.add_argument("--temperature", type=float, default=0.0)
    parser.add_argument("--timeout", type=float, default=600)
    parser.add_argument("--stream", action="store_true")
    parser.add_argument("--prompt", default="Explain KV cache in local LLM serving in three concise bullets.")
    parser.add_argument("--prompt-repeat", type=int, default=1, help="Repeat prompt text to create larger prefill workloads.")
    parser.add_argument("--pid-file", default="state/vllm-metal.pid")
    parser.add_argument("--jsonl", default=None, help="Write raw per-request results as JSONL.")
    args = parser.parse_args()

    if args.warmup:
        await run_batch(args, measured=False)

    before = process_sample(args.pid_file)
    wall_start = time.perf_counter()
    results = await run_batch(args, measured=True)
    wall_s = time.perf_counter() - wall_start
    after = process_sample(args.pid_file)

    if args.jsonl:
        path = Path(args.jsonl)
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w") as file:
            for result in results:
                file.write(json.dumps(asdict(result)) + "\n")

    print(json.dumps(summarize(args, results, wall_s, before, after), indent=2))


if __name__ == "__main__":
    asyncio.run(main())
