#!/usr/bin/env bash
# PreToolUse hook (Edit|Write|MultiEdit): keep the gitnexus code-graph trailing-
# fresh WHILE implementing. When a SOURCE file is about to change and the last
# sync is older than the debounce window, kick a background re-index so the NEXT
# round of context/impact queries reflects what we just wrote.
#
# Always non-blocking and always `exit 0` — it must never delay or veto an edit.
# (For a *guaranteed*-fresh graph at the start of a code task, run the blocking
#  `just gitnexus-sync` and let it finish before your impact/context queries.)
set -u

REPO="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
SYNC="$REPO/.claude/hooks/gitnexus-sync.sh"
MARKER="$REPO/.gitnexus/.last-sync"
LOG="/tmp/astrostack-gitnexus-sync.log"
DEBOUNCE=90 # at most one auto-sync per 90s of active editing

payload="$(cat 2>/dev/null || true)"

# Extract the edit target (jq when present; tolerant grep fallback otherwise).
fp=""
if command -v jq >/dev/null 2>&1; then
  fp="$(printf '%s' "$payload" | jq -r '.tool_input.file_path // .tool_input.path // empty' 2>/dev/null)"
fi
if [[ -z "$fp" ]]; then
  fp="$(printf '%s' "$payload" | grep -oE '"file_path"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed -E 's/.*"([^"]*)"$/\1/')"
fi

# Only OUR source files warrant a code-graph refresh.
case "$fp" in
*.go | *.vue | *.ts | *.tsx | *.js | *.jsx | *.mjs | *.cjs | *.sql) ;;
*) exit 0 ;;
esac

# Never trigger on edits inside excluded trees (vendored / data / build).
case "$fp" in
*/mcp-servers/gimp/* | */input/* | */library/* | */work/* | */output/* | */node_modules/* | */dist/*) exit 0 ;;
esac

# Debounce against the last sync.
now=$(date +%s)
last=0
[[ -f "$MARKER" ]] && last=$(cat "$MARKER" 2>/dev/null || echo 0)
((now - last < DEBOUNCE)) && exit 0

# Claim the window immediately (so a burst of edits triggers ONE sync), then
# refresh in the background. The edit itself proceeds without waiting.
mkdir -p "$REPO/.gitnexus" 2>/dev/null || true
echo "$now" >"$MARKER" 2>/dev/null || true
("$SYNC" >"$LOG" 2>&1 &) >/dev/null 2>&1
exit 0
