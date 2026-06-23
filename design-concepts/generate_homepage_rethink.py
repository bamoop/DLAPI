#!/usr/bin/env python3
import base64
import json
import os
import subprocess
import sys
import time
import urllib.request
from pathlib import Path


BASE_URL = os.environ.get("IMG_BASE_URL", "https://us.zzshu.cc/v1").rstrip("/")
API_KEY = os.environ.get("IMG_API_KEY")
MODEL = os.environ.get("IMG_MODEL", "gpt-image-2")
SIZE = os.environ.get("IMG_SIZE", "1440x1024")
OUT_DIR = Path(__file__).resolve().parent / "homepage-rethink"


PROMPT = """Create one high-end production-quality homepage first-screen UI effect image for DLAPI, an AI API gateway and AI asset management platform.

Do NOT use generic SaaS card-grid landing pages, frosted glass, ice glass, liquid glass, ordinary AI startup gradients, dashboard-only admin UI, people, mascots, stock photos, decorative blobs, or cyberpunk clutter.

Project function:
DLAPI provides one compatible API for 40+ AI providers: OpenAI, Claude, Gemini, DeepSeek, Qwen, Azure OpenAI, AWS Bedrock. It manages routing, model pricing, API keys, quotas, billing, usage logs, observability, rate limits, and security.

Design goal:
Redesign the homepage hero with a distinctive futuristic product identity. It should feel technical, premium, memorable, and implementable, not template-like.

Visual direction:
Dark precision interface, not a dark dashboard. Base palette: deep graphite, black-blue, near-black, electric cyan, signal green, tiny amber warning accents. Add engineered grid lines, thin vector conduits, telemetry ticks, layered depth. Main visual: a central DLAPI geometric routing core, like a technical control module, with luminous vector paths connecting client sources to model/provider nodes. Use a distinctive hexagonal or octagonal brand motif.

Layout:
1440x1024 desktop web homepage, no browser chrome. Top nav: DLAPI, Models, Pricing, Docs, Console, GitHub, Start routing. Left content: badge "AI Gateway / new-api compatible"; headline "One API. Every model. Routed with intent."; copy "Connect providers, keys, quotas, billing, and observability behind one intelligent gateway."; buttons Start routing and View docs; compact proof points: 40+ providers, 99.95% success, real-time logs, cost-aware routing.

Right/main visual: large central DLAPI routing core. Left input nodes: Web App, Backend, Agent, CLI. Right provider nodes: OpenAI, Claude, Gemini, DeepSeek, Qwen, Azure, Bedrock. Add live metrics: P95 latency 112ms, tokens 2.64B, spend today $1,245, healthy channels 38/40. Lower band: Smart routing, Unified API, Cost control, Observability, integrated into the surface, not generic cards.

UI quality:
Strong hierarchy, polished spacing, realistic labels, crisp typography. Do not overcrowd. No lorem ipsum. Make it look like a real product homepage a team would implement. Output only the UI design image."""


def request_json(url: str, payload: dict) -> dict:
    proc = subprocess.run(
        [
            "curl",
            "-sS",
            "--http1.1",
            "--connect-timeout",
            "30",
            "--max-time",
            "420",
            "-X",
            "POST",
            url,
            "-H",
            f"Authorization: Bearer {API_KEY}",
            "-H",
            "Content-Type: application/json",
            "-H",
            "Accept: application/json",
            "-H",
            "User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
            "--data-binary",
            json.dumps(payload),
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or f"curl exited {proc.returncode}")
    try:
        parsed = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Non-JSON response: {proc.stdout[:500]}") from exc
    if isinstance(parsed, dict) and parsed.get("error"):
        raise RuntimeError(json.dumps(parsed["error"], ensure_ascii=False))
    return parsed


def download(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "Codex/1.0"})
    with urllib.request.urlopen(req, timeout=300) as response:
        return response.read()


def extract_image(response: dict) -> bytes:
    data = response.get("data")
    if data:
        first = data[0]
        if first.get("b64_json"):
            return base64.b64decode(first["b64_json"])
        if first.get("url"):
            return download(first["url"])
    if response.get("b64_json"):
        return base64.b64decode(response["b64_json"])
    if response.get("url"):
        return download(response["url"])
    raise RuntimeError(f"Unsupported image response shape: {response}")


def main() -> int:
    if not API_KEY:
        print("IMG_API_KEY is required", file=sys.stderr)
        return 2
    started = time.time()
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    payload = {"model": MODEL, "prompt": PROMPT, "size": SIZE, "n": 1}
    print(f"Generating homepage rethink with {MODEL} at {BASE_URL}...")
    response = request_json(f"{BASE_URL}/images/generations", payload)
    path = OUT_DIR / "dlapi-homepage-tech-core.png"
    path.write_bytes(extract_image(response))
    print(f"saved: {path}")
    print(f"done in {time.time() - started:.1f}s")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
