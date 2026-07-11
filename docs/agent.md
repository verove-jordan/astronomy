# The local AI agent

Everything "agentic" in AstroStack runs against a **local, OpenAI-compatible vision model** — no
cloud calls, opt-in everywhere, and every path soft-fails to the deterministic pipeline. One model
serves three surfaces: the **finish supervisor**, **AstroAgent chat**, and **supervised
conversations** (steerable runs).

## Bring up the model

| Where | Backend | How |
|---|---|---|
| macOS | native **mlx-vlm** (Metal) | `just run-ia-model` (first run downloads ~28 GB) · `ASTRO_LLM_URL=http://127.0.0.1:1234/v1`, `ASTRO_LLM_MODEL=mlx-community/Qwen2.5-VL-32B-Instruct-6bit` |
| Linux + NVIDIA GPU | **Ollama** container | `just stack-ai` (or `just ai-up`) + `just ai-pull` · `ASTRO_LLM_URL=http://ai:11434/v1`, `ASTRO_LLM_MODEL=qwen2.5vl:32b` |
| anywhere | any OpenAI-compatible server | set `ASTRO_LLM_URL` + `ASTRO_LLM_MODEL` |

`just ia-model-status` health-checks it; `/api/agent/status` is what the UI polls.

## The finish supervisor (render → judge → re-tune → keep best)

Opt-in per run (Import checkbox · `process … --supervise` · `just refine <run-dir>`). The finish
becomes a bounded optimisation loop (`internal/pipeline/supervise*.go`):

1. **Render** a candidate finish.
2. **Measure** it deterministically (`measureFinish`: clipping, casts, star quality, detail index)
   → a 0–10 guardrail score.
3. **Critique** with the vision model: it sees the whole-frame thumbnail **and a 100 % centre
   crop**, the measured metrics, the last 6 passes' param diffs + scores, and the best-so-far
   image; it returns fixed-vocabulary defects, a score and a **tiered parameter patch**.
4. **Combined score = 0.6·deterministic + 0.4·model** — a clipped or cast render can never win on
   the model's vote alone. Apply the patch at the cheapest tier and repeat, keeping the best pass.

**Tiers** bound how much work a re-entry may redo — **A**: re-render the GIMP composite only
(seconds) · **B**: redo the linear prep (combine, gradients, SPCC, stretch) · **C**: re-stack from
the raw frames. Budgets: 3×B, 2×C, iteration cap 4 (hard max 8), plateau stop after two
non-improving passes. Every knob change goes through a whitelisted, clamped patch surface
(`internal/pipeline/params_patch.go`) — the same one the UI's advanced editor and per-stage rerun
use. **Warm start**: a rerun of the same target seeds from its best prior pass
(`finish_iterations` history; disable with `ASTRO_SUPERVISE_HISTORY=off`).

The loop is **mode-generic**: deep-sky/nebula re-enter the staged finish; comet re-combines its
star/comet masters; milkyway regrades the persisted linear sky/foreground; planetary re-finishes
the persisted masters. New modes plug in a `candidateRenderer`. A run never fails because of the
agent — any error falls back to the standard finish, and with the model unreachable the output is
byte-identical to a non-supervised run.

**Refine** (`just refine <run-dir>`, or the job page's Refine button) re-runs *only* the finish on
an existing run — no re-stack; **Retry tuned** re-processes with explicit params. **Series
campaigns** chain goal-driven retries into a durable "keep improving this target" loop
(`/api/series`: auto-continue, max attempts, target score, best-job tracking).

## AstroAgent chat (`/astroagent`)

A ReAct-style loop (`internal/agent`) over the same model, with live tools wired to the running
services: jobs (list/launch/tune), mode params, setup and environment, sky/conditions, files and
storage. Guardrails:

- **Read tools run freely; every mutating tool is confirmation-gated** — the UI shows the action
  and you approve or reject it.
- ≤ 12 steps per turn; responses stream over SSE.
- An attached image is first measured (background, casts, clipping, gradient, trails) and the
  numbers are given to the model as ground truth beside the pixels — critiques are anchored, not
  hallucinated.

## Supervised conversations

Every supervised or refine job also surfaces as a **live conversation**: each pass appears as a
chat bubble (preview + defects + scores), you can **nudge** the next iterations in free text
("cooler background, keep the core"), **stop** the loop, and — when enabled — the expensive Tier-C
re-stack asks for confirmation (bounded: 10 minutes, then it proceeds). Headless runs degrade
gracefully (no conversation, same loop).

## Configuration

`ASTRO_LLM_URL` · `ASTRO_LLM_MODEL` · `ASTRO_LLM_IMAGE_FORMAT` (`openai`|`mlxvlm`) ·
`ASTRO_LLM_TIMEOUT_SEC` · `ASTRO_LLM_ASSIST_PROMPT_EXTRA` · `ASTRO_SUPERVISE_HISTORY` — see
[configuration.md](configuration.md). Per-run knobs (`Supervise*`) are in each mode doc's preset
table.
