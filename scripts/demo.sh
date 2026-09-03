#!/usr/bin/env bash
# Record a professional demo video of the AstroStack web UI from a YAML scenario. Like the other
# host-engine glue (Siril/GraXpert/ffmpeg), the recorder runs on the HOST: it drives a real Chromium
# (Playwright) against the running dev app and renders an MP4 with the host ffmpeg. This lives in
# scripts/ (not as repo code) and is invoked by `just demo`.
#
# Usage:  scripts/demo.sh <scenario> [--headless] [--out file]
#   <scenario> is a name under tools/demo/scenarios/ (e.g. overview, tour) or a path to a .yaml.
#
# Preconditions: the app must be up — Postgres (just up), the API (just dev, :8080) and the Vite UI
# (just web, :5173). The script checks both HTTP endpoints and bails with a hint if either is down.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_DIR="$REPO_ROOT/tools/demo"

API_URL="${ASTRO_DEMO_API:-${VITE_API_BASE:-http://localhost:8080}}"

# Reachable = curl gets any HTTP status (000 means connection refused / no server).
reachable() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 4 "$1" || true)"
  [ "$code" != "000" ] && [ -n "$code" ]
}

# Try the Vite dev server (just web) then the containerized frontend (just stack / just web-prod):
# both serve the real app, and refusing to record against a healthy `just stack` helps nobody.
CANDIDATES=()
[ -n "${ASTRO_DEMO_WEB:-}" ] && CANDIDATES+=("$ASTRO_DEMO_WEB")
CANDIDATES+=("http://localhost:${WEB_PORT:-5173}" "http://localhost:${WEB_PORT_PROD:-8082}")

WEB_URL=""
for candidate in "${CANDIDATES[@]}"; do
  if reachable "$candidate"; then
    WEB_URL="$candidate"
    break
  fi
done

if [ -z "$WEB_URL" ]; then
  echo "error: web UI not reachable — tried ${CANDIDATES[*]}" >&2
  echo "  bring the app up:  just up && just dev (terminal 1) && just web (terminal 2)" >&2
  echo "  or all in Docker:  just stack" >&2
  echo "  or point at it explicitly:  ASTRO_DEMO_WEB=http://localhost:PORT just demo" >&2
  exit 1
fi
export ASTRO_DEMO_WEB="$WEB_URL"
if ! reachable "$API_URL"; then
  echo "error: API not reachable at $API_URL  (start it with: just dev)" >&2
  exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
  echo "error: pnpm not found. Install it (corepack enable) — the recorder is a pnpm/TS package." >&2
  exit 1
fi

# First run: install deps + the Playwright Chromium (postinstall handles the browser download).
if [ ! -d "$DEMO_DIR/node_modules" ]; then
  echo "==> installing demo recorder deps (first run; downloads a Chromium build)"
  (cd "$DEMO_DIR" && pnpm install)
fi

cd "$DEMO_DIR"
exec pnpm exec tsx src/index.ts "$@"
