# Demo-video recorder

Scenario-driven recorder that drives the AstroStack web UI in a real browser and renders a polished
**MP4** — animated cursor, lower-third captions, intro/outro cards, optional music + voiceover, and a
**×7 time-lapse** of the live stacking job. Playwright drives + records; the host `ffmpeg` assembles.

This is a host tool (it drives a real Chromium and the host ffmpeg), kept out of the app bundle. No
Python — TypeScript only, per the repo language policy.

## Quick start

```bash
# 1. Bring the app up (three terminals): Postgres, API, Vite UI
just up && just migrate
just dev          # API on :8080
just web          # UI  on :5173

# 2. Record. First run installs deps + a Chromium build.
just demo tour              # deterministic: replays an existing finished run (no capture data needed)
just demo overview          # full tour incl. a live ×7 stacking job (needs input/<target> data)
just demo overview --headless
```

Output lands at `output/demo/<scenario>.mp4`. Intermediate clips are under `output/demo/.work/…`
(both git-ignored).

## Authoring scenarios

Scenarios live in `scenarios/*.yaml`. Generate one from the project docs with the Claude command:

```
/demo-video overview --job input/<smallTarget>
/demo-video tour            # no --job → a finished-run fallback demo
```

The schema is defined and validated in `src/scenario.ts`; `scenarios/overview.yaml` (live job) and
`scenarios/tour.yaml` (fallback) are worked examples. Key ideas:

- **Navigate by URL** (`goto: /processing/import`); the UI locale is pinned so text selectors are stable.
- Targets resolve by `testid` (a `data-demo` hook — most robust), `text`/`role`, `css`, or `firstCard`.
- The **live job** is configured entirely from the `job:` block; the recorder fills the Import form via
  the `data-demo` hooks on `ImportView`/`FileBrowser`. Only the `job` step's processing **wait** is
  time-lapsed (`speed: 7`). Point `job.input` at a SMALL capture set.
- Set `meta.voiceover: say` for macOS-TTS narration of `narrate` lines; drop a royalty-free track at
  `assets/music/…` and set `meta.music` for a score.

## How it works

`src/recorder.ts` records one continuous WebM and a timeline of spans (per-span speed / zoom /
narration). `src/postprocess.ts` re-times each span, bookends the intro/outro card clips, mixes audio,
and encodes the final H.264 MP4. See `src/clips.ts` (video), `src/audio.ts` + `src/voiceover.ts` (sound).

```bash
pnpm install            # deps + Chromium (postinstall)
pnpm typecheck          # tsc --noEmit
pnpm exec tsx src/index.ts tour --headless   # run directly (what `just demo` calls)
```

Override binaries with `FFMPEG_BIN` / `FFPROBE_BIN` (same as the engine).
