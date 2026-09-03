#!/usr/bin/env bash
# Stop a device server already holding the sidecar's port, so `just device` / `just device-x86` can
# simply be re-run instead of failing with "address already in use".
#
# This exists because the sidecar is deliberately a long-lived separate process (so `just dev`
# restarting the engine can never drop a USB connection mid-sequence). The cost of that design is
# that it outlives the terminal you started it from, and the next start dies on a bound port while
# the real cause — a stray process from an earlier session — is nowhere in the error message.
set -euo pipefail

addr="${ASTRO_DEVICE_ADDR:-127.0.0.1:8084}"
port="${addr##*:}"

pids="$(lsof -ti "tcp:${port}" -sTCP:LISTEN 2>/dev/null || true)"
[ -n "$pids" ] || exit 0

# Resolve the PID from a port number and check WHAT it is before signalling it. A telescope task
# runner must never shoot an unrelated process that happens to be on this port.
for pid in $pids; do
    cmd="$(ps -p "$pid" -o command= 2>/dev/null || true)"
    case "$cmd" in
    *astrostack*)
        echo "device: stopping the server already on :${port} (pid ${pid})"
        kill "$pid" 2>/dev/null || true
        ;;
    *)
        echo "device: :${port} is held by something that is not astrostack (pid ${pid}):" >&2
        echo "  ${cmd}" >&2
        echo "device: refusing to kill it — stop it yourself, or point ASTRO_DEVICE_ADDR elsewhere." >&2
        exit 1
        ;;
    esac
done

# Wait for the PROCESS to exit, not just for the port to free. Measured: the sidecar releases its
# listener immediately but stays alive afterwards closing devices — and that shutdown is what parks
# the camera's cooler and stops the mount's axes. Starting the replacement during that window would
# hand it a camera and a serial port the old process has not let go of yet.
for _ in $(seq 1 75); do
    alive=0
    for pid in $pids; do
        kill -0 "$pid" 2>/dev/null && alive=1
    done
    [ "$alive" -eq 0 ] && exit 0
    sleep 0.2
done

echo "device: the server on :${port} did not stop within 15s; forcing it" >&2
for pid in $pids; do kill -9 "$pid" 2>/dev/null || true; done
sleep 0.5
