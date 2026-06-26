#!/usr/bin/env bash
# SessionStart hook: if the gitnexus index is missing or stale (>2h), refresh it
# in the BACKGROUND so the first code-graph queries of the session are accurate.
# Non-blocking by design — a 10s-2min re-index must never delay session start.
set -u

REPO="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
NEXUS_DIR="$REPO/.gitnexus"
SYNC="$REPO/.claude/hooks/gitnexus-sync.sh"
LOG="/tmp/astrostack-gitnexus-sync.log"
THRESHOLD=$((2 * 3600)) # 2 hours
NOW=$(date +%s)

# Freshness = newest mtime among the index metadata and our own sync marker.
newest=0
for f in "$NEXUS_DIR/meta.json" "$NEXUS_DIR/.last-sync"; do
  [[ -f "$f" ]] || continue
  mt=$(stat -f %m "$f" 2>/dev/null || stat -c %Y "$f" 2>/dev/null || echo 0)
  ((mt > newest)) && newest=$mt
done

if ((newest > 0 && NOW - newest <= THRESHOLD)); then
  exit 0 # fresh enough — nothing to do
fi

echo "[gitnexus] index missing or >2h stale — refreshing in background (log: $LOG)" >&2
("$SYNC" >"$LOG" 2>&1 &) >/dev/null 2>&1
exit 0
