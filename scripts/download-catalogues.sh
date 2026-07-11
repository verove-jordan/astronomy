#!/usr/bin/env bash
# Download Siril's offline Gaia DR3 catalogues into <ASTRO_LIBRARY_DIR>/catalogues so plate-solving
# (astrometric extract, ~1.1 GB compressed → ~3 GB) and optionally SPCC colour calibration
# (xp_sampled chunks, ~5 GB) work with no network. The engine points Siril at these files via
# `set core.catalogue_gaia_astro/_photo` lines in its generated scripts; the Docker engine sees the
# same files through the /data/library volume. Safe to re-run: existing files are kept, partial
# downloads resume.
#
# Usage: download-catalogues.sh [--spcc] [--dir <catalogues-dir>]
set -euo pipefail

ASTRO_NAME="siril_cat_healpix8_astro.dat"
ASTRO_URL="https://zenodo.org/records/14692304/files/${ASTRO_NAME}.bz2?download=1"
ASTRO_SHA_URL="https://zenodo.org/records/14692304/files/${ASTRO_NAME}.bz2.sha256sum?download=1"
SPCC_API="https://zenodo.org/api/records/14738271"
SPCC_DL_BASE="https://zenodo.org/records/14738271/files"

DIR="${ASTRO_LIBRARY_DIR:-library}/catalogues"
SPCC=false
while [ $# -gt 0 ]; do
  case "$1" in
    --spcc) SPCC=true ;;
    --dir) DIR="$2"; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

mkdir -p "$DIR"

# fetch <url> <out.bz2> [sha_url] — resumable download, optional published-checksum verify of the
# compressed artifact, then decompress in place (keeps only the .dat).
fetch() {
  local url="$1" out="$2" sha_url="${3:-}" dat="${2%.bz2}"
  if [ -s "$dat" ]; then
    echo "already present: $(basename "$dat")"
    return 0
  fi
  echo "downloading $(basename "$out") ..."
  curl -L --fail --retry 5 --retry-delay 5 -C - -o "$out" "$url"
  if [ -n "$sha_url" ]; then
    echo "verifying checksum ..."
    want=$(curl -sL --fail "$sha_url" | awk '{print $1}')
    got=$(shasum -a 256 "$out" | awk '{print $1}')
    if [ "$want" != "$got" ]; then
      echo "ERROR: checksum mismatch for $(basename "$out") (want $want got $got)" >&2
      exit 1
    fi
  fi
  echo "decompressing $(basename "$out") ..."
  bunzip2 -f "$out"
}

# --- astrometric catalogue (plate-solve; the critical one) ---
fetch "$ASTRO_URL" "$DIR/$ASTRO_NAME.bz2" "$ASTRO_SHA_URL"

# --- SPCC xp_sampled chunks (optional; SPCC falls back to online Gaia without them) ---
if $SPCC; then
  echo "listing SPCC xp_sampled chunks ..."
  names=$(curl -sL --fail "$SPCC_API" | python3 -c '
import json, sys
for f in json.load(sys.stdin)["files"]:
    if f["key"].endswith(".dat.bz2"):
        print(f["key"])
')
  total=$(echo "$names" | wc -l | tr -d " ")
  i=0
  for name in $names; do
    i=$((i + 1))
    echo "[$i/$total] $name"
    fetch "$SPCC_DL_BASE/$name?download=1" "$DIR/$name"
  done
fi

echo "done. catalogues in: $DIR"
du -sh "$DIR" 2>/dev/null || true
