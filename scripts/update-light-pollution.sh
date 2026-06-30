#!/usr/bin/env bash
# Download/refresh the OFFLINE light-pollution atlas — the hybrid fallback used by
# internal/lightpollution when the keyed online API is unreachable (see CLAUDE.md / docs).
#
# The atlas is a compact EPSG:4326 row-major float32 grid `atlas.bin` plus a JSON sidecar `atlas.json`
# (rows, cols, lat/lon bounds, unit, nodata) that the Go reader bilinear-samples. It is written into the
# gitignored data dir, never the repo. No secrets are used here — the keyed online API is configured
# separately (ASTRO_LIGHTPOLLUTION_API_KEY) and is the primary source.
#
# Configure ONE source via the environment (.env):
#   ASTRO_LIGHTPOLLUTION_ATLAS_URL       a ready-made atlas.bin (EPSG:4326 row-major float32) — no deps
#   ASTRO_LIGHTPOLLUTION_ATLAS_JSON_URL  its sidecar (paired with the above)
#   ASTRO_LIGHTPOLLUTION_ATLAS_TIFF_URL  a VIIRS / World-Atlas GeoTIFF to convert locally (needs gdal+jq)
set -euo pipefail

DATA_DIR="${ASTRO_DATA_DIR:-./data}"
DEST_DIR="${DATA_DIR}/lightpollution"
BIN="${DEST_DIR}/atlas.bin"
JSON="${DEST_DIR}/atlas.json"
UNIT="${ASTRO_LIGHTPOLLUTION_ATLAS_UNIT:-sqm}" # sqm | bortle | mcd | radiance (how to read cell values)

mkdir -p "$DEST_DIR"

if [[ -n "${ASTRO_LIGHTPOLLUTION_ATLAS_URL:-}" ]]; then
  echo "Fetching pre-gridded light-pollution atlas → $BIN"
  curl -fSL --retry 3 -o "$BIN" "$ASTRO_LIGHTPOLLUTION_ATLAS_URL"
  if [[ -n "${ASTRO_LIGHTPOLLUTION_ATLAS_JSON_URL:-}" ]]; then
    curl -fSL --retry 3 -o "$JSON" "$ASTRO_LIGHTPOLLUTION_ATLAS_JSON_URL"
  fi

elif [[ -n "${ASTRO_LIGHTPOLLUTION_ATLAS_TIFF_URL:-}" ]]; then
  command -v gdalwarp >/dev/null || { echo "ERROR: gdalwarp not found — 'brew install gdal' to convert the GeoTIFF" >&2; exit 1; }
  command -v jq >/dev/null || { echo "ERROR: jq not found — 'brew install jq' to write the atlas sidecar" >&2; exit 1; }
  tmp_tif="$(mktemp -t lpatlas-XXXX).tif"
  trap 'rm -f "$tmp_tif" "${BIN}.hdr" "${BIN}.aux.xml"' EXIT
  echo "Downloading GeoTIFF and reprojecting to an EPSG:4326 float32 grid (gdal)…"
  curl -fSL --retry 3 -o "$tmp_tif" "$ASTRO_LIGHTPOLLUTION_ATLAS_TIFF_URL"
  # ENVI output is a flat, row-major (north-up), little-endian float32 blob — exactly the reader's format.
  gdalwarp -overwrite -t_srs EPSG:4326 -of ENVI -ot Float32 "$tmp_tif" "$BIN" >/dev/null
  gdalinfo -json "$BIN" | jq \
    --arg unit "$UNIT" \
    '{rows: .size[1], cols: .size[0],
      lat_min: .cornerCoordinates.lowerRight[1], lat_max: .cornerCoordinates.upperLeft[1],
      lon_min: .cornerCoordinates.upperLeft[0],  lon_max: .cornerCoordinates.lowerRight[0],
      unit: $unit, nodata: (.bands[0].noDataValue // -1)}' > "$JSON"

else
  cat >&2 <<'EOF'
No OFFLINE atlas source configured — and you do not need one. By default the app already gets
light-pollution data keyless from NASA GIBS VIIRS (map overlay + per-site sky brightness), no setup.

This optional step only installs a higher-precision OFFLINE atlas (e.g. the Falchi World Atlas). Set
ONE of these in your environment / .env, then re-run:
  ASTRO_LIGHTPOLLUTION_ATLAS_URL       a ready-made atlas.bin (+ ASTRO_LIGHTPOLLUTION_ATLAS_JSON_URL)
  ASTRO_LIGHTPOLLUTION_ATLAS_TIFF_URL  a VIIRS / World-Atlas GeoTIFF to convert (needs gdal + jq)
EOF
  exit 0
fi

[[ -f "$JSON" ]] || echo "WARNING: $JSON missing — the reader needs the sidecar (rows/cols/bounds/unit)." >&2
echo "Done — $(du -h "$BIN" | cut -f1) atlas at $BIN (unit=${UNIT})."
