#!/usr/bin/env bash
# Build/refresh the OFFLINE tree-canopy-height atlas — the accuracy source for the dark-sky finder's
# tree-aware horizon (internal/canopy + internal/elevation; see CLAUDE.md / docs). A spot hemmed in by a
# forest then scores its low horizon correctly: a 20 m treeline 30 m away blocks ~34° of sky.
#
# The atlas is a compact EPSG:4326 row-major float32 grid `atlas.bin` + a JSON sidecar `atlas.json`
# (rows, cols, lat/lon bounds, unit="meters", nodata) that the Go reader (internal/geogrid) samples. It is
# written where the engine reads it (ASTRO_CANOPY_ATLAS, else $ASTRO_WORK_DIR/canopy) — never the repo.
# Restart the engine (or Docker `engine`) afterwards to load it. Canopy is OPTIONAL and soft-fails: with no
# atlas the horizon stays terrain-only, exactly as before.
#
# Configure ONE source via the environment (.env):
#   ASTRO_CANOPY_ATLAS_URL       a ready-made atlas.bin (EPSG:4326 row-major float32, metres) — no deps
#   ASTRO_CANOPY_ATLAS_JSON_URL  its sidecar (paired with the above)
#   ASTRO_CANOPY_ATLAS_TIFF_URL  a canopy-height GeoTIFF/VRT/`/vsicurl/…` to convert locally (needs gdal+jq)
#                                e.g. an ETH Global Canopy Height 2020 (Lang et al., 10 m, CC BY 4.0) tile
# Optional (convert mode): ASTRO_CANOPY_BBOX="minLon minLat maxLon maxLat" to clip; ASTRO_CANOPY_RES_DEG to
# downsample (default 0.0008° ≈ 90 m — France ≈ ~1 GB; native 10 m would be ~80 GB). Downsampling uses
# `-r max` (worst-case): each cell keeps the TALLEST canopy, because obstruction is a worst-case, not a mean.
set -euo pipefail

# Write where the engine reads: ASTRO_CANOPY_ATLAS (exact file) wins, else <WorkDir>/canopy/atlas.bin.
if [[ -n "${ASTRO_CANOPY_ATLAS:-}" ]]; then
  BIN="$ASTRO_CANOPY_ATLAS"
  DEST_DIR="$(dirname "$BIN")"
else
  DEST_DIR="${ASTRO_WORK_DIR:-./work}/canopy"
  BIN="${DEST_DIR}/atlas.bin"
fi
JSON="${BIN%.*}.json"
UNIT="${ASTRO_CANOPY_ATLAS_UNIT:-meters}"
RES="${ASTRO_CANOPY_RES_DEG:-0.0008}" # ~90 m; blank to keep native resolution
BBOX="${ASTRO_CANOPY_BBOX:-}"         # "minLon minLat maxLon maxLat" (EPSG:4326) to clip, or blank

mkdir -p "$DEST_DIR"

if [[ -n "${ASTRO_CANOPY_ATLAS_URL:-}" ]]; then
  echo "Fetching pre-gridded canopy atlas → $BIN"
  curl -fSL --retry 3 -o "$BIN" "$ASTRO_CANOPY_ATLAS_URL"
  if [[ -n "${ASTRO_CANOPY_ATLAS_JSON_URL:-}" ]]; then
    curl -fSL --retry 3 -o "$JSON" "$ASTRO_CANOPY_ATLAS_JSON_URL"
  fi

elif [[ -n "${ASTRO_CANOPY_ATLAS_TIFF_URL:-}" ]]; then
  command -v gdalwarp >/dev/null || { echo "ERROR: gdalwarp not found — 'brew install gdal' to convert the GeoTIFF" >&2; exit 1; }
  command -v jq >/dev/null || { echo "ERROR: jq not found — 'brew install jq' to write the atlas sidecar" >&2; exit 1; }
  trap 'rm -f "${BIN}.hdr" "${BIN}.aux.xml"' EXIT
  echo "Reprojecting canopy-height raster to an EPSG:4326 float32 grid (gdal, -r max)…"
  # -r max keeps the tallest canopy per output cell; ENVI output is the flat north-up little-endian float32
  # blob the reader expects. /vsicurl/ lets the TIFF URL stream without a full download.
  # shellcheck disable=SC2086
  gdalwarp -overwrite -t_srs EPSG:4326 -r max -ot Float32 -of ENVI \
    ${RES:+-tr $RES $RES} ${BBOX:+-te $BBOX} \
    "/vsicurl/${ASTRO_CANOPY_ATLAS_TIFF_URL#/vsicurl/}" "$BIN" >/dev/null
  gdalinfo -json "$BIN" | jq \
    --arg unit "$UNIT" \
    '{rows: .size[1], cols: .size[0],
      lat_min: .cornerCoordinates.lowerRight[1], lat_max: .cornerCoordinates.upperLeft[1],
      lon_min: .cornerCoordinates.upperLeft[0],  lon_max: .cornerCoordinates.lowerRight[0],
      unit: $unit, nodata: (.bands[0].noDataValue // -1)}' > "$JSON"

else
  cat >&2 <<'EOF'
No canopy atlas source configured — and it is OPTIONAL. Without it the dark-sky finder's horizon stays
terrain-only (exactly as before); with it, nearby forests correctly lower a site's low horizon.

Set ONE of these in your environment / .env, then re-run and restart the engine:
  ASTRO_CANOPY_ATLAS_URL       a ready-made atlas.bin (+ ASTRO_CANOPY_ATLAS_JSON_URL)
  ASTRO_CANOPY_ATLAS_TIFF_URL  a canopy-height GeoTIFF/VRT to convert (needs gdal + jq)
                               e.g. an ETH Global Canopy Height 2020 tile (10 m, metres, CC BY 4.0)
Optional: ASTRO_CANOPY_BBOX="minLon minLat maxLon maxLat", ASTRO_CANOPY_RES_DEG (default 0.0008 ≈ 90 m).
EOF
  exit 0
fi

[[ -f "$JSON" ]] || echo "WARNING: $JSON missing — the reader needs the sidecar (rows/cols/bounds/unit)." >&2
echo "Done — $(du -h "$BIN" | cut -f1) canopy atlas at $BIN (unit=${UNIT}). Restart the engine to load it."
