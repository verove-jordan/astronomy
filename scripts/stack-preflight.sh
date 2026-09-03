#!/usr/bin/env bash
# Preflight for `just stack`: everything that must be true BEFORE docker compose is asked to build.
#
# It exists because a fresh clone is missing the things that command silently depends on. None of
# input/ output/ work/ library/ is tracked by git, so a clone has none of them and Docker quietly
# invents them as bind-mount sources; .env is git-ignored, so it is absent too. Neither failure is
# loud — you get an empty file browser and a stack that half-works — which is the worst way for a
# new collaborator to meet a project.
#
# Everything here is idempotent: it says what it changed, stays quiet about what was already right,
# and is safe to run against a stack that is already up.
set -euo pipefail

cd "$(dirname "$0")/.."

say()  { printf '  %s\n' "$1"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; }
did()  { printf '  \033[36m•\033[0m %s\n' "$1"; }
warn() { printf '  \033[33m⚠\033[0m %s\n' "$1"; }
die()  { printf '\n  \033[31m✗ %s\033[0m\n\n' "$1" >&2; exit 1; }

printf '\n\033[1m▸ Preflight\033[0m\n'

# --- Docker ----------------------------------------------------------------------------------
# `docker info` and not `docker --version`: the CLI answers the version question perfectly well
# while the daemon is stopped, which is the actual failure mode on a Mac that just booted.
command -v docker >/dev/null 2>&1 || die "Docker is not installed. Install Docker Desktop: https://docs.docker.com/desktop/install/mac-install/"
if ! docker info >/dev/null 2>&1; then
    die "Docker is installed but not running — start Docker Desktop, wait for the whale icon to settle, then re-run \`just stack\`."
fi
docker compose version >/dev/null 2>&1 || die "The Docker Compose v2 plugin is missing. Docker Desktop ships it; on Linux install docker-compose-plugin."
ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo running) running"

# --- .env ------------------------------------------------------------------------------------
# Compose reads .env for interpolation and just loads it into recipes, so its absence is not fatal
# — every variable has a default. It is created anyway, because it is the one file the user is
# expected to edit, and telling them to copy it by hand is a step that can simply not exist.
if [ ! -f .env ]; then
    cp .env.example .env
    did "created .env from .env.example"
fi

# A pre-existing .env may carry the loopback LLM URL, which is correct on the host and wrong in a
# container — there it points the engine at itself. Newly-created .env files no longer set it.
if grep -qE '^[[:space:]]*ASTRO_LLM_URL=http://(127\.0\.0\.1|localhost):' .env 2>/dev/null; then
    warn "your .env pins ASTRO_LLM_URL to loopback, which inside the container means the container itself."
    say  "  Comment it out to reach a model running on your Mac (the stack then uses host.docker.internal)."
fi

# --- data directories ------------------------------------------------------------------------
# Mounted at their identical host paths (compose.yaml) so absolute paths stored in Postgres and
# run.json resolve the same in both modes. Created here rather than left to Docker so ownership is
# the user's and the set is complete.
created=()
for d in input library output work; do
    [ -d "$d" ] || { mkdir -p "$d"; created+=("$d/"); }
done
[ ${#created[@]} -eq 0 ] || did "created ${created[*]}"

# --- ports -----------------------------------------------------------------------------------
# A port already taken makes `up` fail with a message about the container, not about the thing
# actually holding the port. Checked here so the fix can be named.
port_free() {
    if command -v lsof >/dev/null 2>&1; then
        ! lsof -nP -iTCP:"$1" -sTCP:LISTEN -t >/dev/null 2>&1
    elif command -v nc >/dev/null 2>&1; then
        ! nc -z 127.0.0.1 "$1" >/dev/null 2>&1
    else
        return 0 # no way to tell; let compose speak for itself
    fi
}

# Read one KEY=VALUE from .env, falling back to a default. Deliberately not `source .env` — that
# would execute whatever is in there and export it into compose's environment.
envval() {
    local v
    # Last assignment wins (as with any env file), trailing `# comment` and quotes stripped.
    v="$(sed -n "s/^[[:space:]]*$1=//p" .env 2>/dev/null | tail -1 | sed 's/[[:space:]]*#.*$//' | tr -d '"'"'"' \r')"
    [ -n "$v" ] && printf '%s' "$v" || printf '%s' "$2"
}

running="$(docker compose ps --status running --format '{{.Service}}' 2>/dev/null || true)"
clash=0
free_ports=()
held_ports=()
check_port() {           # check_port <service> <port> <env-var>
    # A service of ours that is already up legitimately holds its port; re-running `just stack`
    # must not report the stack against itself. Said separately from "free", because "free" would
    # be a plain untruth about a port something is listening on.
    if grep -qx "$1" <<<"$running"; then
        held_ports+=("$2")
        return 0
    fi
    if port_free "$2"; then
        free_ports+=("$2")
        return 0
    fi
    warn "port $2 is already in use (needed by '$1') — set $3 in .env to a free port"
    clash=1
}
pg_port="$(envval POSTGRES_PORT 5432)"
en_port="$(envval ENGINE_PORT 8080)"
web_port="$(envval WEB_PORT_PROD 8082)"
check_port db "$pg_port" POSTGRES_PORT
check_port engine "$en_port" ENGINE_PORT
check_port frontend "$web_port" WEB_PORT_PROD
[ "$clash" -eq 1 ] && die "Free those ports (or change them in .env) and re-run \`just stack\`."
[ ${#free_ports[@]} -eq 0 ] || ok "ports ${free_ports[*]} free"
[ ${#held_ports[@]} -eq 0 ] || ok "ports ${held_ports[*]} already served by this stack (it will be updated in place)"

# --- disk ------------------------------------------------------------------------------------
# The engine image carries GIMP, GDAL and onnxruntime; with the build cache a first run wants
# appreciably more room than the final image suggests.
avail_kb="$(df -Pk . | awk 'NR==2 {print $4}')"
avail_gb=$(( avail_kb / 1024 / 1024 ))
if [ "$avail_gb" -lt 15 ]; then
    warn "only ${avail_gb} GB free — the first build needs roughly 15 GB (\`docker system prune\` reclaims space)"
else
    ok "${avail_gb} GB free disk"
fi
