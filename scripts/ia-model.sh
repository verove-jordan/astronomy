#!/usr/bin/env bash
# Serve the local vision model the optional finish supervisor calls. Like Siril/GraXpert it is a
# host-engine tool — invoked, never vendored — so this lives in scripts/, not as Python in the repo.
#
# Idempotent: creates .venv-ia and installs mlx-vlm on first run, then serves an OpenAI-compatible
# endpoint (/v1/models, /health, /v1/chat/completions). Model weights auto-download from Hugging Face
# on first request (~26 GB for the 6-bit 32B default). Foreground; Ctrl-C to stop.
set -euo pipefail

MODEL="${ASTRO_LLM_MODEL:-mlx-community/Qwen2.5-VL-32B-Instruct-6bit}" # same id the engine sends
PORT="${ASTRO_LLM_PORT:-1234}"
VENV="${ASTRO_IA_VENV:-.venv-ia}"
PY="${PYTHON_BIN:-python3.12}"

# macOS ships its CA bundle at /etc/ssl/cert.pem. Some shells preset REQUESTS_CA_BUNDLE/SSL_CERT_FILE
# to the Linux path (/etc/ssl/certs/ca-certificates.crt), which does not exist here and breaks TLS for
# BOTH pip and the runtime Hugging Face download. Repoint any bundle that points at a missing file to a
# valid one (or unset it) so certificate verification works.
for _ca in REQUESTS_CA_BUNDLE SSL_CERT_FILE CURL_CA_BUNDLE; do
  _val="${!_ca:-}"
  if [ -n "$_val" ] && [ ! -f "$_val" ]; then
    if [ -f /etc/ssl/cert.pem ]; then
      export "$_ca=/etc/ssl/cert.pem"
    else
      unset "$_ca"
    fi
  fi
done
unset _ca _val

if ! command -v "$PY" >/dev/null 2>&1; then
  echo "error: '$PY' not found. Install it (brew install python@3.12) or set PYTHON_BIN." >&2
  exit 1
fi

if [ ! -d "$VENV" ]; then
  echo "==> creating venv $VENV"
  "$PY" -m venv "$VENV"
fi

if ! "$VENV/bin/python" -c 'import mlx_vlm' >/dev/null 2>&1; then
  echo "==> installing mlx-vlm into $VENV (first run only)"
  "$VENV/bin/pip" install -U pip mlx-vlm
fi

echo "==> serving $MODEL on http://127.0.0.1:$PORT  (Ctrl-C to stop)"
echo "    first run downloads weights from Hugging Face (~26 GB for the 6-bit 32B)."

# mlx-vlm exposes the server as a console script (mlx_vlm.server); fall back to the module form.
if [ -x "$VENV/bin/mlx_vlm.server" ]; then
  exec "$VENV/bin/mlx_vlm.server" --model "$MODEL" --host 127.0.0.1 --port "$PORT"
fi
exec "$VENV/bin/python" -m mlx_vlm.server --model "$MODEL" --host 127.0.0.1 --port "$PORT"
