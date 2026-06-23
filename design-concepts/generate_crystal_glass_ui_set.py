#!/usr/bin/env python3
import base64
import json
import os
import subprocess
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path


BASE_URL = os.environ.get("IMG_BASE_URL", "https://us.zzshu.cc/v1").rstrip("/")
API_KEY = os.environ.get("IMG_API_KEY")
MODEL = os.environ.get("IMG_MODEL", "gpt-image-2")
SIZE = os.environ.get("IMG_SIZE", "1440x1024")
OUT_DIR = Path(__file__).resolve().parent / "crystal-glass-ui-set"


STYLE_PROMPT = """Create a high-end production SaaS UI screen for DLAPI, an AI API gateway and AI asset management platform based on new-api.

Visual source analysis to follow:
- Futuristic ice-glass / liquid-glass UI style.
- Bright soft white and pale blue environment, luminous frosted background, subtle prismatic refractions, caustic highlights, thin cyan glow.
- Large translucent rounded glass panels with thick beveled transparent borders, inner white highlights, very soft shadows, and layered depth.
- UI modules look like clear acrylic floating over a bright cold-light surface.
- Elegant blue-to-cyan accents, small emerald status dots, occasional amber warnings.
- Central gateway capsule or orb can appear where useful, with fiber-optic routing lines flowing between apps and AI providers.
- The feeling is premium, airy, technical, and clean, not dark cyberpunk.
- Typography is crisp, modern, product-like, with readable 14-16px body text and bold large headings where appropriate.

Product context:
- DLAPI routes one compatible API to 40+ AI providers.
- It manages OpenAI, Claude, Gemini, DeepSeek, Qwen, Azure OpenAI, AWS Bedrock and other channels.
- It includes provider channels, model routing, API keys, quotas, billing, user groups, logs, rate limits, OAuth/passkeys, and observability.

Hard UI requirements:
- Target dimensions: 1440x1024 desktop web app screen.
- No browser chrome, no device frame.
- Do not create a marketing-only hero unless the screen is explicitly Home.
- Do not use random stock photos, people, mascots, decorative blobs, or dark neon cyberpunk.
- Do not copy an existing layout exactly, but preserve the visual language described above.
- Keep data realistic and readable. Use short real UI labels, not lorem ipsum.
- Use clear product components: navigation, chips, tabs, filters, KPI cards, glass tables, provider rows, model badges, charts, routing flow diagrams, quota rings, timeline logs.
- Avoid clutter: high-end, airy, and luminous while still operational.
"""


SCREENS = [
    {
        "slug": "01-home",
        "name": "Home",
        "prompt": """Screen: Home landing / product entry.
Design the first screen for DLAPI. Include logo, top navigation: Models, Pricing, Docs, Console. Main headline: "One API for every model". Subtitle about routing 40+ providers with compatible API, keys, quotas, billing, and observability. Primary action "Start routing", secondary "View docs". Center-right hero visual: a crystal gateway capsule labeled DLAPI AI Gateway, with luminous fiber-optic streams connecting left app sources (Web App, Mobile App, Backend Service, Agent/Bot, CLI/SDK) to right provider pills (OpenAI, Claude, Gemini, DeepSeek, Qwen, Azure OpenAI, AWS Bedrock). Bottom row of glass feature tiles: One API, Smart Routing, Real-time Observability, Usage & Cost Control, Enterprise Ready.""",
    },
    {
        "slug": "02-dashboard",
        "name": "Dashboard",
        "prompt": """Screen: Operations dashboard overview.
Create a live overview console with translucent glass cards. Top: Overview, Live badge, time range selector, auto refresh toggle. KPI strip: Requests/min 12,458, Success rate 99.95%, P95 latency 112 ms, Spend 15m $18.53, Tokens 15m 2.64B. Middle: traffic flow diagram with app sources routed through central DLAPI gateway to provider distribution. Right: provider health table with status, latency, degraded AWS Bedrock row. Bottom: live activity table with timestamps, endpoint, model, provider, status, latency, tokens, cost. Add spend-over-time glass chart.""",
    },
    {
        "slug": "03-routing-atlas",
        "name": "Model Routing",
        "prompt": """Screen: Model Routing / Routing Atlas.
Create a visual routing workbench. Header "Routing Atlas" with tabs All Routes, Fallbacks, Guardrails, Latency, Cost, Optimize for Quality. Left source stack: Web App, Mobile App, Backend, Agent/Bot with rpm. Center: glowing DLAPI gateway orb. Curved colored fiber routes split to provider cards: OpenAI gpt-4o, Claude claude-3.5-sonnet, Gemini gemini-1.5-pro, DeepSeek deepseek-v3, Other Providers. Show route weights, rpm, priority route, fallback route, and optimization chips: Latency Lowest, Quality Highest, Cost Optimized, Reliability Balanced. Bottom-right button "Routing Rules".""",
    },
    {
        "slug": "04-keys-quotas",
        "name": "Keys & Quotas",
        "prompt": """Screen: API Keys and Quotas management.
Design an admin page for API key governance. Header "Keys & Quotas", action "Create Key". Top glass key cards for Production Web, Mobile App, Backend Service, Internal Tools, each with masked key, active badge, 24h requests. Middle quota overview with circular glass progress rings: Requests 64%, Tokens 52%, Spend 41%, Errors 0.8%. Lower section rate limit sliders/controls: Requests/min, Tokens/min, Burst, Window. Bottom permissions and groups chips/cards: Admin, Developers, Billing, Observers. Keep it luminous and airy with blue/cyan progress arcs and emerald active dots.""",
    },
    {
        "slug": "05-billing-logs",
        "name": "Billing & Logs",
        "prompt": """Screen: Billing and Usage Logs.
Create a combined financial observability screen. Header "Billing & Logs", tabs Usage Logs, Spend, Billing Rules, Invoices, Reports, filters button. Top metrics: Total Spend $1,245.18, Total Requests 12.45M, Total Tokens 2.64B, Avg Cost / 1K Tokens $0.04218 with mini sparklines. Main area: request timeline table with success dots, endpoint, model, provider, status, latency, tokens, cost. Right glass panel: Request Detail JSON preview and Billing Expression formula with computed cost. Bottom trace flow: Client -> DLAPI Gateway -> OpenAI -> Response 200 OK.""",
    },
    {
        "slug": "06-system-settings",
        "name": "System Settings",
        "prompt": """Screen: System Settings and Provider Configuration.
Design a polished settings workspace in the same ice-glass style. Left translucent settings navigation: Site & Branding, Authentication, Billing & Payment, Models & Routing, Security & Limits, Console Content, Operations. Main panel: provider channel configuration with status cards for OpenAI, Claude, Gemini, DeepSeek, Qwen, Azure OpenAI, AWS Bedrock. Include model mapping preview, routing priority, fallback enabled toggles, health checks, balance query, and upstream sync status. Right side: security limits card with rate limits, content guardrail toggles, OAuth/passkey status, audit retention. Use crisp enterprise settings layout, not a hero page.""",
    },
]


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


def generate(screen: dict) -> tuple[str, Path]:
    prompt = f"{STYLE_PROMPT}\n\n{screen['prompt']}"
    payload = {
        "model": MODEL,
        "prompt": prompt,
        "size": SIZE,
        "n": 1,
    }
    response = request_json(f"{BASE_URL}/images/generations", payload)
    image = extract_image(response)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    path = OUT_DIR / f"{screen['slug']}.png"
    path.write_bytes(image)
    return screen["name"], path


def main() -> int:
    if not API_KEY:
        print("IMG_API_KEY is required", file=sys.stderr)
        return 2

    screen_filter = os.environ.get("SCREEN_FILTER")
    screens = SCREENS
    if screen_filter:
        screens = [
            screen
            for screen in SCREENS
            if screen_filter.lower() in screen["slug"].lower()
            or screen_filter.lower() in screen["name"].lower()
        ]
        if not screens:
            print(f"No screens match SCREEN_FILTER={screen_filter!r}", file=sys.stderr)
            return 2

    started = time.time()
    print(f"Generating {len(screens)} crystal-glass screens with {MODEL} at {BASE_URL}...")
    with ThreadPoolExecutor(max_workers=min(6, len(screens))) as executor:
        futures = [executor.submit(generate, screen) for screen in screens]
        for future in as_completed(futures):
            name, path = future.result()
            print(f"saved: {name} -> {path}")
    print(f"done in {time.time() - started:.1f}s")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
