#!/usr/bin/env bash
# Entrypoint for the containerized AstroStack engine. Prepares the writable runtime dirs the pipeline
# needs (bind mounts can start empty), then execs the API server. All paths come from env — see the
# defaults baked in docker/engine.Dockerfile; override any via compose/.env.
set -euo pipefail

WORK="${ASTRO_WORK_DIR:-/data/work}"

# The pipeline writes here during a run. input/ is bind-mounted by compose (read-write, so the S3
# import can land capture folders in it) and so is never created here.
mkdir -p "$WORK" "${ASTRO_OUTPUT_DIR:-/data/output}" "${ASTRO_LIBRARY_DIR:-/data/library}"

# Siril and GIMP write their config/cache/venv under $HOME by default. When the container runs as an
# overridden host UID (Linux, to match bind-mount ownership) $HOME may not be writable, so redirect all
# of it into the always-writable work volume. This keeps Siril's sirilpy venv (plate-solve/SPCC) and
# the GIMP profile working regardless of the runtime UID.
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$WORK/.config}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$WORK/.cache}"
# XDG_DATA_HOME too: GraXpert downloads its AI models to ~/.local/share/GraXpert on first use —
# without this redirect they land in the ephemeral container FS and re-download on every recreate.
export XDG_DATA_HOME="${XDG_DATA_HOME:-$WORK/.local/share}"
export GIMP2_DIRECTORY="${GIMP2_DIRECTORY:-$WORK/.gimp-2.10}"
mkdir -p "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$GIMP2_DIRECTORY"

# Siril's SPCC sensor/filter database: baked into the image (a headless Siril never downloads it —
# without it `spcc` aborts even on a plate-solved image and colour falls back to star-field gains).
# Symlinked, not copied, into Siril's user data dir ($XDG_DATA_HOME/siril) so it survives work-volume
# resets and always matches the image's pinned revision.
if [ -d /opt/siril-spcc-database ]; then
  mkdir -p "$XDG_DATA_HOME/siril"
  ln -sfn /opt/siril-spcc-database "$XDG_DATA_HOME/siril/siril-spcc-database"
fi

# serve reads all config from the environment and self-migrates the DB on startup.
exec astrostack serve
