#!/usr/bin/env bash
# Entrypoint for the containerized AstroStack engine. Prepares the writable runtime dirs the pipeline
# needs (bind mounts can start empty), then execs the API server. All paths come from env — see the
# defaults baked in docker/engine.Dockerfile; override any via compose/.env.
set -euo pipefail

WORK="${ASTRO_WORK_DIR:-/data/work}"

# The pipeline writes here during a run; input/ is mounted read-only and is not created.
mkdir -p "$WORK" "${ASTRO_OUTPUT_DIR:-/data/output}" "${ASTRO_LIBRARY_DIR:-/data/library}"

# Siril and GIMP write their config/cache/venv under $HOME by default. When the container runs as an
# overridden host UID (Linux, to match bind-mount ownership) $HOME may not be writable, so redirect all
# of it into the always-writable work volume. This keeps Siril's sirilpy venv (plate-solve/SPCC) and
# the GIMP profile working regardless of the runtime UID.
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$WORK/.config}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$WORK/.cache}"
export GIMP2_DIRECTORY="${GIMP2_DIRECTORY:-$WORK/.gimp-2.10}"
mkdir -p "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$GIMP2_DIRECTORY"

# serve reads all config from the environment and self-migrates the DB on startup.
exec astrostack serve
