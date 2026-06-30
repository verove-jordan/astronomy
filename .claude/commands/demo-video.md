---
description: Generate a YAML demo-video scenario for the AstroStack web UI from the project docs
argument-hint: "[name] [--lang en|fr] [--job <input-dir>] [--focus tonight,import,...]"
allowed-tools: Read, Glob, Grep, Write, Bash, mcp__chrome__navigate_page, mcp__chrome__take_snapshot, mcp__chrome__list_pages
---

You are authoring a **demo-video scenario** — a YAML walkthrough of the AstroStack web UI that the
recorder in `tools/demo/` turns into a narrated MP4 (`just demo <name>`). Produce the YAML; do not run
the recorder.

Arguments: `$ARGUMENTS`
Parse them as: a scenario **name** (default `overview`); `--lang en|fr` (default `en`); `--job <dir>` a
capture folder to stack live (omit → a finished-run fallback demo); `--focus <csv>` to restrict which
pages are featured.

## 1. Learn the product (read, don't guess)

Read these to script an accurate tour and write truthful captions:
- `README.md` — the **"The web UI"** section enumerates every page (Planner: Tonight/GoTo/Calendar;
  Processing: Import/Live/Tasks/Runs/Library) with a one-line purpose each. This is your primary source.
- `frontend/src/router/index.ts` — the **canonical routes** to navigate by URL.
- `frontend/src/i18n/<lang>.json` — exact UI wording. Pull captions/labels from `nav.*`,
  `processing.tabs.*`, `common.*`, and each page's namespace so the copy matches the screen. The `fr.json`
  keys mirror `en.json`, so `--lang fr` captions come from the same keys.
- `docs/pipeline.md` (optional) — for accurate processing-stage wording (calibrate → register → stack →
  finish; the four modes deepsky/nebula/milkyway/planetary).

Optionally confirm a real finished run exists for the fallback: `curl -s "$VITE_API_BASE"/api/runs`
(default `http://localhost:8080/api/runs`), or open the app with the `chrome` MCP to eyeball selectors.

## 2. Know the schema (it is validated — match it exactly)

`tools/demo/src/scenario.ts` is the source of truth. A scenario is:

```yaml
meta:
  title: string
  lang: en|fr
  viewport: [1920, 1080]
  scale: 2
  fps: 30
  baseWeb: http://localhost:5173
  baseApi: http://localhost:8080
  # music: assets/music/<track>.mp3   # optional; user supplies the file
  voiceover: say|none                 # 'say' = macOS TTS narration of `narrate` lines
intro: { title, subtitle?, seconds }
steps:
  - name: short-id
    caption: "on-screen lower-third text"        # optional
    narrate: "spoken line (used when voiceover=say)"  # optional
    goto: /route                                  # optional, app path
    tab: "Tab label"                              # optional, clicks a role=tab
    click:    { text|css|testid|firstCard, role?, exact?, nth? }
    type:     { into: <target>, text: "…", enter: true|false }
    hover:    <target>
    scrollTo: <target>
    highlight: <target>                           # spotlight until the next step
    job:      { input: "input/M101", mode: deepsky, format: image, options?: {…}, run?: <target> }
    waitForJob: { until: complete|percent, percent?, maxSeconds }
    speed: 7                                       # post-process time-lapse for THIS step's span
    zoom:  { target?: <target>, scale: 1.25 }     # Ken-Burns push-in
    dwell: 2.5                                     # seconds to hold after the action
outro: { title, subtitle?, seconds }
fallbackRun: <step>                               # runs only when NO step has a `job:` block
```

A `<target>` resolves by, in order of preference: `testid` (a `data-demo` attribute — most robust),
`text` (+ optional `role`), `css`, or `firstCard: true` (the first run card on `/processing/runs`).

### Selector rules (critical — the app has almost no test hooks)

- **Navigate by URL** (`goto`), never by clicking nav links. Routes: `/tonight`, `/goto`, `/calendar`,
  `/processing/import`, `/processing/live`, `/processing/tasks`, `/processing/runs`,
  `/processing/library`.
- The UI locale is **pinned** by the recorder, so `text` selectors are stable in the chosen `--lang`.
- For the **live job**, prefer the `job:` block — the recorder fills the Import form via these existing
  `data-demo` hooks: `browse-path`, `browse-inspect`, `run-mode`, `run-format`, `run-pipeline`,
  `opt-<name>` (e.g. `opt-denoise`). You don't wire these yourself; just provide `job.input/mode/format`.
- Only the **job/process** span should carry `speed: 7`. Keep `job.input` a SMALL capture set so the
  real-time span is short before the ×7 compression.

## 3. Compose the narrative

Default arc (trim to `--focus` if given), ~60–90 s final:
intro card → `/tonight` (plan/scoring) → open a target (highlight + zoom) → `/calendar` (almanac) →
`/goto` (alignment) → `/processing/import` (auto-sort from FITS) → **live job ×7** (if `--job` given,
else skip) → result panels (zoom) → `/processing/runs` (gallery) → outro card.

- Captions: short, concrete, grounded in the docs. Narration (`narrate`) only on a few key beats.
- If `--job` is **absent**, do NOT add a `job:` step. Instead end the `steps` on `/processing/runs` and
  add a `fallbackRun` that does `click: { firstCard: true }` + a zoom — a deterministic finished-run demo.
- Mirror the structure of `tools/demo/scenarios/overview.yaml` (live job) and `tour.yaml` (fallback).

## 4. Write it

Write to `tools/demo/scenarios/<name>.yaml`. Then print, verbatim, the next step:

> Recorded with: `just demo <name>` — make sure the app is up (`just up && just dev` + `just web`).
> For the score, drop a royalty-free track at `tools/demo/assets/music/…` and set `meta.music`.

Keep the YAML clean and commented at the top with what it demonstrates and how to regenerate it.
