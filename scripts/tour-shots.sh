#!/usr/bin/env bash
# Regenerate the in-app tour screenshots. Drives the running web UI in a real Chromium (Playwright),
# photographs one image per tour step with the focus highlight baked in, and writes them to
# frontend/public/tour/<locale>/. Host tool, exactly like scripts/demo.sh — it needs a real browser
# and the host ffmpeg.
#
# Usage:  scripts/tour-shots.sh [scenario] [--headed] [--locales en,fr]
#   [scenario] is a name under tools/demo/scenarios/ (default: tour-shots) or a path to a .yaml.
#
# Preconditions: the app must be up — Postgres (just up), the API (just dev, :8080) and the Vite UI
# (just web, :5173). Shots are only as good as the data behind them: a run in the Tasks list and a
# capture folder in the data dir make the Import/Job pages photograph something real.
#
# Commit frontend/public/tour/ afterwards — the tour has to work from a clone.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_DIR="$REPO_ROOT/tools/demo"

API_URL="${ASTRO_DEMO_API:-${VITE_API_BASE:-http://localhost:8080}}"

reachable() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 4 "$1" || true)"
  [ "$code" != "000" ] && [ -n "$code" ]
}

# The app is served two different ways and BOTH are the real thing: the Vite dev server on
# WEB_PORT (just web) and the containerized nginx frontend on WEB_PORT_PROD (just stack /
# just web-prod). Try each in turn rather than assuming the dev server — assuming it is why this
# script used to refuse to run against a perfectly healthy `just stack`.
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
  echo "  or point at it explicitly:  ASTRO_DEMO_WEB=http://localhost:PORT just tour-shots" >&2
  exit 1
fi
if ! reachable "$API_URL"; then
  echo "error: API not reachable at $API_URL  (start it with: just dev, or just stack)" >&2
  exit 1
fi
echo "==> shooting against $WEB_URL"
# Hand the resolved URL to the generator: its scenario carries its own baseWeb default, so without
# this the check above could pass while the browser still went to a port nothing is serving.
export ASTRO_DEMO_WEB="$WEB_URL"

if ! command -v pnpm >/dev/null 2>&1; then
  echo "error: pnpm not found. Install it (corepack enable) — the recorder is a pnpm/TS package." >&2
  exit 1
fi

if [ ! -d "$DEMO_DIR/node_modules" ]; then
  echo "==> installing demo recorder deps (first run; downloads a Chromium build)"
  (cd "$DEMO_DIR" && pnpm install)
fi

cd "$DEMO_DIR"
exec pnpm exec tsx src/shots.ts "$@"
