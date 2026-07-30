#!/usr/bin/env bash
# PreToolUse(Bash) guard: test/debug image-processing runs must go through the job
# API (POST /api/jobs) so they surface in the frontend and can be monitored manually.
# Inline `astrostack|just process|refine|video` is denied with a redirect. Override a
# genuinely-required inline run by prefixing ASTRO_INLINE_OK=1 (after the user's OK).
set -euo pipefail

cmd="$(cat | jq -r '.tool_input.command // ""')"
[ -z "$cmd" ] && exit 0
case "$cmd" in *ASTRO_INLINE_OK=1*) exit 0 ;; esac   # consented escape hatch

# astrostack binary (go run / built) or a `just` recipe, then process|refine|video.
if printf '%s' "$cmd" | grep -Eq \
  '(go[[:space:]]+run[[:space:]]+(\./)?cmd/astrostack[^[:space:]]*|(\./)?bin/astrostack|(^|[[:space:]])astrostack|(^|[[:space:]])just)[[:space:]]+(process|refine|video)([[:space:]]|$)'; then
  reason='Run image processing as a monitorable JOB, not inline — inline astrostack/just process|refine|video is one-shot and invisible to the frontend (no progress, preview, SSE, pause/cancel). Instead POST a job to the running engine: POST http://localhost:8080/api/jobs with JSON {"path","mode","format"} (path inside ASTRO_DATA_DIR, e.g. input/<target>), then watch GET /api/jobs/{id}/events — it appears in the UI Tasks view for manual monitoring. See the "Test/debug image processing" rule in CLAUDE.md for the exact curl. Ensure `just dev` is running (host :8080). If an inline run is genuinely required, re-run with ASTRO_INLINE_OK=1 prefixed after getting the user'\''s OK.'
  jq -n --arg r "$reason" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:$r}}'
  exit 0
fi
exit 0
