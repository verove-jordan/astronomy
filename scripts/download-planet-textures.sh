#!/usr/bin/env bash
# Download the surface maps the 3-D solar-system page draws its worlds with.
#
# The maps are fetched rather than vendored: they are tens of megabytes and carry their own licence,
# so they live under the work directory beside the light-pollution and canopy atlases and stay out of
# both the repository and the Docker build context. Everything is optional — a body whose map is
# missing is shaded procedurally, exactly as a missing StarNet++ falls back to full stars.
#
# Source: Solar System Scope (https://www.solarsystemscope.com/textures/), CC BY 4.0. The page shows
# that attribution in its legend; see docs/third-party.md.
set -uo pipefail

DIR="${ASTRO_WORK_DIR:-./work}/solarsystem"
BASE="https://www.solarsystemscope.com/textures/download"
RES="${1:-2k}"

mkdir -p "$DIR"

# remote file -> the texture key the engine serves it under. The key is what internal/solarsystem's
# body table names, so renaming one side without the other simply falls back to procedural shading.
MAPS=(
  "${RES}_sun.jpg:sun"
  "${RES}_mercury.jpg:mercury"
  "${RES}_venus_atmosphere.jpg:venus"
  "${RES}_earth_daymap.jpg:earth"
  "${RES}_earth_nightmap.jpg:earth_night"
  "${RES}_moon.jpg:moon"
  "${RES}_mars.jpg:mars"
  "${RES}_jupiter.jpg:jupiter"
  "${RES}_saturn.jpg:saturn"
  "${RES}_saturn_ring_alpha.png:saturn_ring"
  "${RES}_uranus.jpg:uranus"
  "${RES}_neptune.jpg:neptune"
)

ok=0
skipped=0
failed=0

for entry in "${MAPS[@]}"; do
  remote="${entry%%:*}"
  key="${entry##*:}"
  ext="${remote##*.}"
  dest="$DIR/$key.$ext"

  # Idempotent: a map already on disk is not re-fetched. Re-running after a partial download picks up
  # only what is missing, which is the whole point of running it twice.
  if [ -s "$dest" ]; then
    skipped=$((skipped + 1))
    continue
  fi

  # Download to a temp name in the SAME directory and rename into place. The engine reads this folder
  # while jobs run, and a half-written JPEG that a reader can see is worse than one that is absent.
  tmp="$dest.part.$$"
  if curl -fsSL --max-time 180 -o "$tmp" "$BASE/$remote"; then
    mv -f "$tmp" "$dest"
    printf '  %-12s %s\n' "$key" "$(du -h "$dest" | cut -f1)"
    ok=$((ok + 1))
  else
    rm -f "$tmp"
    echo "  $key: download failed (skipped — this body will be shaded procedurally)" >&2
    failed=$((failed + 1))
  fi
done

echo
echo "planet textures in $DIR — $ok downloaded, $skipped already present, $failed unavailable"
echo "Restart the engine to pick them up (the texture directory is scanned once at startup)."
echo "Maps © Solar System Scope, CC BY 4.0 — the page credits this in its legend."
