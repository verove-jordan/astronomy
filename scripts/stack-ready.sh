#!/usr/bin/env bash
# The other half of `just stack`: wait for the engine, report what the container can actually do,
# and print the URL.
#
# `docker compose up -d` returns as soon as the containers are CREATED, which on a first run is a
# long way from serving: the engine still has to connect to Postgres and apply 44 migrations. So
# the command used to end on a wall of compose output with nothing to open and no way to tell
# whether it had worked. This waits for /api/health, then runs `astrostack doctor` inside the
# engine so the tools reported are the ones that will really run the jobs, and ends on the link.
set -euo pipefail

cd "$(dirname "$0")/.."

# First boot runs migrations against a cold database; be patient before calling it broken.
readonly HEALTH_TIMEOUT=${STACK_HEALTH_TIMEOUT:-180}

# curl is how readiness is judged; without it the wait below could only ever time out.
command -v curl >/dev/null 2>&1 || {
    printf '\n  The stack is starting, but curl is missing so readiness cannot be checked.\n'
    printf '  Give it a minute, then open http://localhost:8082\n\n'
    exit 0
}

envval() {
    local v
    # Last assignment wins, trailing `# comment` and quotes stripped.
    v="$(sed -n "s/^[[:space:]]*$1=//p" .env 2>/dev/null | tail -1 | sed 's/[[:space:]]*#.*$//' | tr -d '"'"'"' \r')"
    [ -n "$v" ] && printf '%s' "$v" || printf '%s' "$2"
}
en_port="$(envval ENGINE_PORT 8080)"
web_port="$(envval WEB_PORT_PROD 8082)"

printf '\n\033[1m▸ Waiting for the engine\033[0m\n'
start=$(date +%s)
until curl -fsS "http://localhost:${en_port}/api/health" >/dev/null 2>&1; do
    elapsed=$(( $(date +%s) - start ))
    if [ "$elapsed" -ge "$HEALTH_TIMEOUT" ]; then
        printf '  \033[31m✗ the engine did not become healthy within %ss\033[0m\n\n' "$HEALTH_TIMEOUT" >&2
        printf '  Last 40 lines of its log:\n\n' >&2
        docker compose --profile stack logs --tail=40 engine >&2 || true
        printf '\n  Full log: just stack-logs\n\n' >&2
        exit 1
    fi
    printf '\r  … %ss' "$elapsed"
    sleep 2
done
printf '\r  \033[32m✓\033[0m ready (%ss)\n' "$(( $(date +%s) - start ))"

# The report comes from INSIDE the container on purpose: the tools that matter are the Linux ones
# baked into the image, not whatever happens to be installed on the Mac. Tolerated if it fails —
# `doctor` exits non-zero when Siril is unavailable, and that must still leave the user with a URL
# and a readable reason rather than a bare non-zero exit from `just`.
printf '\n\033[1m▸ Environment\033[0m (inside the engine container)\n'
docker compose --profile stack exec -T engine astrostack doctor || true

# nginx serves the built SPA and proxies /api to the engine; it starts fast but is worth confirming,
# since it is the address the user is about to be handed.
if ! curl -fsS -o /dev/null "http://localhost:${web_port}/" 2>/dev/null; then
    for _ in 1 2 3 4 5; do
        sleep 2
        curl -fsS -o /dev/null "http://localhost:${web_port}/" 2>/dev/null && break
    done
fi

printf '\n\033[1m▸ AstroStack is running\033[0m\n\n'
printf '     \033[1;36mhttp://localhost:%s\033[0m\n\n' "$web_port"
printf '  API   http://localhost:%s/api/health\n' "$en_port"
printf '  Logs  just stack-logs        Stop  just stack-down\n\n'
printf '  Put a capture folder in ./input, then open Processing → Import.\n\n'
