# syntax=docker/dockerfile:1
#
# Containerized AstroStack engine (`astrostack serve`: HTTP API + worker) with the Linux builds of the
# tools it drives baked in — Siril, GIMP, GraXpert, ffmpeg. This is the portable / server-deploy path;
# daily macOS dev still uses the host-run engine (the "host-engine exception" in CLAUDE.md). No Go code
# changes: every tool path is a config env var, set to the Linux install below.
#
# The VLM (finish supervisor) is NOT in this image — it's a separate, opt-in OpenAI-compatible server
# the engine reaches over ASTRO_LLM_URL (native mlx on macOS, or the `ai` compose service on Linux+GPU).

########## builder — compile the Go engine (static, no cgo) ##########
FROM golang:1.23-bookworm AS builder
WORKDIR /src
# Dependency layer first so source edits don't bust the module cache.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Build identity: .dockerignore excludes .git, so the describe string arrives as a build arg
# (compose passes GIT_DESCRIBE; a bare `docker build` gets "dev").
ARG GIT_DESCRIBE=dev
ARG BUILD_TIME=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w \
      -X github.com/verove-jordan/astronomy/internal/buildinfo.Version=${GIT_DESCRIBE} \
      -X github.com/verove-jordan/astronomy/internal/buildinfo.BuiltAt=${BUILD_TIME}" \
      -o /out/astrostack ./cmd/astrostack

########## runtime — engine + host tools on a glibc base ##########
FROM ubuntu:24.04 AS runtime
ENV DEBIAN_FRONTEND=noninteractive

# System tools the pipeline shells out to: GIMP 2.10 console (matches the host), ffmpeg, python3.12
# (GraXpert + Siril's sirilpy for plate-solve/SPCC), plus libs the extracted Siril AppImage needs and
# wget for the healthcheck.
RUN apt-get update && apt-get install -y --no-install-recommends \
      gimp \
      ffmpeg \
      python3.12 python3.12-venv python3-pip \
      ca-certificates wget curl tzdata \
      libgtk-3-0 libgomp1 \
 && rm -rf /var/lib/apt/lists/*

# --- Siril: installed to match the image architecture, so the engine runs NATIVELY on the host arch
# (amd64 on a Linux server, arm64 on Apple Silicon) — no emulation.
#   • amd64 → the host's 1.4.x x86_64 AppImage, extracted (best output parity; Siril ships only x86_64).
#   • arm64 → the maintainer PPA (ppa:lock042/siril) 1.4.x build. Siril ships no arm64 AppImage, and the
#     distro 'universe' package is only 1.2.1 — too old for the 1.4 script syntax the pipeline emits
#     (e.g. `rgbcomp -lum=/-out=`), which 1.2.x silently ignores, breaking the finish. See README
#     "version-drift". Override the amd64 URL / arm64 PPA with --build-arg to track the host exactly.
# Either arch ends up at /usr/local/bin/siril-cli with its catalogue dir linked at /opt/siril-catalogue.
ARG TARGETARCH
ARG SIRIL_VERSION=1.4.3
ARG SIRIL_APPIMAGE_URL=https://free-astro.org/download/Siril-1.4.3-x86_64.AppImage
ARG SIRIL_PPA=ppa:lock042/siril
RUN set -eux; \
    if [ "${TARGETARCH:-amd64}" = "amd64" ]; then \
      apt-get update && apt-get install -y --no-install-recommends squashfs-tools binutils && rm -rf /var/lib/apt/lists/*; \
      cd /tmp; \
      wget -q -O siril.AppImage "$SIRIL_APPIMAGE_URL"; \
      # Extract the squashfs payload WITHOUT executing the AppImage runtime (so it also works when
      # cross-building amd64 on an arm64 host). The squashfs is appended right after the ELF image.
      shoff="$(readelf -h siril.AppImage | awk '/Start of section headers/ {print $5}')"; \
      shent="$(readelf -h siril.AppImage | awk '/Size of section headers/ {print $5}')"; \
      shnum="$(readelf -h siril.AppImage | awk '/Number of section headers/ {print $5}')"; \
      unsquashfs -d /opt/siril -o "$(( shoff + shnum * shent ))" siril.AppImage >/dev/null; \
      rm -f siril.AppImage; \
      test -x /opt/siril/usr/bin/siril-cli || { echo "siril-cli not found in AppImage"; ls -R /opt/siril/usr/bin || true; exit 1; }; \
      { \
        echo '#!/bin/sh'; \
        echo 'APPDIR=/opt/siril'; \
        echo 'export LD_LIBRARY_PATH="$APPDIR/usr/lib:$APPDIR/usr/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"'; \
        echo 'export GSETTINGS_SCHEMA_DIR="$APPDIR/usr/share/glib-2.0/schemas"'; \
        echo 'export XDG_DATA_DIRS="$APPDIR/usr/share:${XDG_DATA_DIRS:-/usr/local/share:/usr/share}"'; \
        echo 'exec "$APPDIR/usr/bin/siril-cli" "$@"'; \
      } > /usr/local/bin/siril-cli; \
      chmod +x /usr/local/bin/siril-cli; \
      ln -sfn /opt/siril/usr/share/siril /opt/siril-catalogue; \
    else \
      # arm64: add the maintainer PPA so apt installs Siril 1.4.x (the noble 'universe' package is 1.2.1,
      # too old for the 1.4 script syntax the pipeline emits). software-properties-common provides
      # add-apt-repository.
      apt-get update && apt-get install -y --no-install-recommends software-properties-common; \
      add-apt-repository -y "$SIRIL_PPA"; \
      apt-get update && apt-get install -y --no-install-recommends siril && rm -rf /var/lib/apt/lists/*; \
      sirilbin="$(command -v siril-cli || true)"; \
      [ -n "$sirilbin" ] || { echo "ERROR: the 'siril' package provided no siril-cli on ${TARGETARCH}"; dpkg -L siril | grep -iE 'bin/siril' || true; exit 1; }; \
      ln -sfn "$sirilbin" /usr/local/bin/siril-cli; \
      ln -sfn /usr/share/siril /opt/siril-catalogue; \
    fi; \
    # Native build → this runs. Kept non-fatal so a cross-arch build (image arch ≠ host) still completes.
    { /usr/local/bin/siril-cli --version && echo "siril-cli OK"; } || \
      echo "WARN: siril-cli --version failed — expected only when the image arch != the build host; verify natively"

# --- GraXpert: optional AI background-extraction / denoise (soft-fails to Siril subsky when absent).
# Installed in an isolated venv (Ubuntu 24.04 is PEP-668 externally-managed). Set --build-arg
# INSTALL_GRAXPERT=false for a slimmer image; the pipeline then falls back automatically.
ARG INSTALL_GRAXPERT=true
RUN if [ "$INSTALL_GRAXPERT" = "true" ]; then \
      python3.12 -m venv /opt/graxpert-venv; \
      /opt/graxpert-venv/bin/pip install --no-cache-dir --upgrade pip; \
      # onnxruntime pinned explicitly: `pip install graxpert` alone does not reliably resolve a working
      # wheel on every arch/python combo, and a GraXpert with a broken ONNX runtime fails EVERY
      # extraction at runtime while still exiting 0 — worse than GraXpert being absent (the engine's
      # graxpert.Healthy probe then disables the AI path). The import check below makes the IMAGE BUILD
      # fail loudly instead of shipping a silently-broken tool.
      /opt/graxpert-venv/bin/pip install --no-cache-dir graxpert "onnxruntime>=1.18,<2"; \
      /opt/graxpert-venv/bin/python -c "import onnxruntime, graxpert; print('graxpert OK, onnxruntime', onnxruntime.__version__)"; \
      ln -s /opt/graxpert-venv/bin/graxpert /usr/local/bin/graxpert; \
    fi

# --- LibRaw's dcraw_emu: develops iPhone DNG/HEIC (and other camera raws) to a 16-bit RGB TIFF Siril
# imports — the Linux stand-in for the host's macOS `sips`, since Siril's own libraw can't decode iPhone
# DNG ("no RAW data available"). Kept in its own layer so it doesn't invalidate the expensive Siril /
# GraXpert layers above on a rebuild. rawconv auto-selects it via DCRAW_BIN when sips is absent.
RUN apt-get update && apt-get install -y --no-install-recommends libraw-bin \
 && rm -rf /var/lib/apt/lists/*

# --- GDAL (gdal-bin): the canopy-height atlas downloader shells out to gdalwarp/gdalbuildvrt/gdalinfo to
# stream + reproject the ETH 10 m canopy COGs over /vsicurl/ (internal/canopy/build.go — the DarkSky
# finder's "download canopy for this area" button). Resolved by bare name on PATH (no *_BIN var), so
# gdal-bin in /usr/bin is all that's needed; the feature soft-fails to a terrain-only horizon when absent.
# Own layer (like libraw-bin) so it doesn't bust the Siril/GraXpert cache on rebuild.
RUN apt-get update && apt-get install -y --no-install-recommends gdal-bin \
 && rm -rf /var/lib/apt/lists/*

# StarNet++ is deliberately NOT baked in (its licence isn't redistributable). To enable star removal,
# bind-mount your StarNet install and set STARNET_BIN; until then the pipeline keeps full stars.

# --- engine binary + entrypoint, run as a non-root user ---
RUN useradd --create-home --uid 10001 app
COPY --from=builder /out/astrostack /usr/local/bin/astrostack
COPY docker/engine-entrypoint.sh /usr/local/bin/engine-entrypoint
RUN chmod +x /usr/local/bin/engine-entrypoint

# Baked defaults — all overridable via compose/.env. Tool paths point at the Linux installs above; the
# data dirs live under /data (bind-mounted); DB + LLM URLs are set by compose to the `db`/`ai` services
# or the host. ASTRO_SIRIL_CATALOG_DIR must be set explicitly (the macOS-bundle default doesn't apply);
# it points at the `catalogue` SUBDIR (where the CSVs live), matching the host default. On amd64 the
# 1.4.x AppImage ships those CSVs there; on arm64 the distro Siril ships a legacy .txt format the
# parser can't read, so the engine falls back to the catalogue snapshot embedded in the binary
# (internal/skycat) — the tonight planner + name→coord resolver work either way.
ENV SIRIL_BIN=/usr/local/bin/siril-cli \
    ASTRO_SIRIL_CATALOG_DIR=/opt/siril-catalogue/catalogue \
    GIMP_BIN=/usr/bin/gimp-console-2.10 \
    GRAXPERT_BIN=graxpert \
    DCRAW_BIN=dcraw_emu \
    FFMPEG_BIN=ffmpeg \
    ASTRO_DATA_DIR=/data/input \
    ASTRO_WORK_DIR=/data/work \
    ASTRO_OUTPUT_DIR=/data/output \
    ASTRO_LIBRARY_DIR=/data/library \
    API_ADDR=:8080

# Pre-create /data owned by the app user so the `work` named volume (initialized from this path) is
# writable; the scratch dir is sticky-world-writable so an overridden host UID (Linux bind mounts) can
# still write Siril/GIMP config there (the entrypoint points XDG_* at it). input/library/output are
# bind-mounted over their subdirs at runtime.
RUN mkdir -p /data/input /data/work /data/output /data/library \
 && chown -R 10001:10001 /data \
 && chmod 1777 /data/work

USER app
WORKDIR /home/app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s \
  CMD wget -qO- http://localhost:8080/api/health >/dev/null 2>&1 || exit 1
ENTRYPOINT ["engine-entrypoint"]
