#!/usr/bin/env bash
# Incrementally refresh the gitnexus code-graph index for THIS repo.
#
# Single source of truth for "make the graph reflect current code". Called from:
#   - on demand:    just gitnexus-sync          (foreground; surfaces failures)
#   - SessionStart:  session-start-gitnexus.sh   (background, only if stale)
#   - before edits:  pre-edit-gitnexus.sh        (background, debounced)
#
# Concurrency-safe (mkdir-lock) and degrades gracefully when gitnexus is absent.
# Flags:  -f | --force  -> full re-index (gitnexus analyze --force).
set -u

REPO="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
GITNEXUS_BIN="${GITNEXUS_BIN:-gitnexus}"
NEXUS_DIR="$REPO/.gitnexus"
LOCK="$NEXUS_DIR/.sync.lock"
MARKER="$NEXUS_DIR/.last-sync"
STALE_LOCK=$((10 * 60)) # reclaim a lock orphaned by a crashed run after 10 min

force=""
case "${1:-}" in -f | --force) force="--force" ;; esac

if ! command -v "$GITNEXUS_BIN" >/dev/null 2>&1; then
  echo "[gitnexus] binary '$GITNEXUS_BIN' not found — skipping sync (graph features off)" >&2
  exit 0
fi

mkdir -p "$NEXUS_DIR" 2>/dev/null || true

# Reclaim a stale lock left behind by a crashed run.
if [[ -d "$LOCK" ]]; then
  lock_mt=$(stat -f %m "$LOCK" 2>/dev/null || stat -c %Y "$LOCK" 2>/dev/null || echo 0)
  if (($(date +%s) - lock_mt > STALE_LOCK)); then
    rmdir "$LOCK" 2>/dev/null || true
  fi
fi

# Atomic lock: exactly one racer wins; the rest bow out quietly.
if ! mkdir "$LOCK" 2>/dev/null; then
  echo "[gitnexus] a sync is already running — skipping" >&2
  exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT

echo "[gitnexus] $(date '+%Y-%m-%d %H:%M:%S') analyze ${force:-(incremental)} $REPO" >&2
"$GITNEXUS_BIN" analyze $force --skip-agents-md "$REPO"
rc=$?
date +%s >"$MARKER" 2>/dev/null || true
echo "[gitnexus] $(date '+%Y-%m-%d %H:%M:%S') done (rc=$rc)" >&2
exit $rc
