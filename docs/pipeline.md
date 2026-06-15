# Pipeline

How `astrostack process <dir>` turns a capture folder into a finished image.

## Deep-sky

1. **Inspect** (`internal/inspect`) — walk the directory, read each FITS header, and classify every
   file as light / dark / flat / bias / dark-flat / video. Frames are grouped into *sets* by
   object, filter, exposure, gain, offset, temperature and binning. Files without an `IMAGETYP`
   card fall back to a sampled-ADU heuristic.

2. **Master calibration** (`internal/calib`) — for each calibration set, stack a master with Siril
   (Winsorized sigma). With a database, masters are saved to a **reusable library** and an existing
   matching master is reused instead of rebuilt; a lights-only session pulls the right masters from
   the library. Matching rules: darks by exposure + temperature (±5 °C) + gain + offset; flats by
   filter; bias by gain + offset.

3. **Per channel** (`internal/pipeline` + `internal/grade`):
   - **Calibrate + register** the lights (Siril `calibrate` then `register`).
   - **Grade** each sub-frame from the registration metrics (FWHM, roundness, star count,
     background) and a pure-Go Hough **trail detector**. Frames are rejected for elongated stars,
     soft focus/seeing, clouds (few stars), or satellite/aircraft trails — robust median+MAD rules
     that never reject a tight set and never reject everything.
   - **Stack** only the survivors (`select`/`unselect` + `stack … -filter-incl`). Winsorized sigma
     also clips residual trail pixels.

4. **Co-register channels** (`internal/pipeline` → `siril.AlignMastersScript`) — the per-channel
   masters are registered together to one reference so L/R/G/B/Ha line up before compositing.

5. **Finish in GIMP** (`internal/gimp`) — Siril background-extracts + stretches each channel to a
   TIFF; the engine then drives the resident GIMP Script-Fu server (shared with the GIMP MCP) to
   build a layered image — RGB base + L in `LAYER-MODE-LUMINANCE` + Ha red-tinted in `SCREEN` —
   apply gentle curves/levels/saturation, and export an editable `.xcf` plus flattened TIFF/PNG.
   If GIMP is unavailable it falls back to the Siril `rgbcomp` finish (`internal/postprocess`).

Each run writes its outputs and a JSON/markdown report; with the API, the full report (including
per-frame grades) is stored on the job and rendered in the web UI's frame-review page.

## Lunar / planetary video

`astrostack video <file>` (`internal/planetary`): extract frames (ffmpeg for MP4/MOV/MKV/AVI; SER
read by Siril) → convert to a FITS sequence → rank frames by Laplacian-variance **sharpness** in Go
→ keep the best N% → stack (no surface alignment) → unsharp + stretch → export.

High-precision multi-point planetary alignment is a known Siril-CLI limitation; for demanding
planetary work use the Siril GUI or AutoStakkert!.

## Modes

`internal/mode` maps each capture mode to a `Preset` that retunes the whole pipeline:

- **deepsky** — mono LRGB+Ha, balanced grading, gentle curves.
- **nebula** — mono LRGB+Ha, lenient grading + stronger background extraction + a heavier Ha
  screen for faint emission.
- **milkyway** — one-shot-color (iPhone ProRAW/HEIC, jpg/png/tif) via `pipeline.ProcessOSC`:
  debayer → register → grade → stack → GIMP curves, with strong gradient removal and natural
  star colors.
- **planetary** — lucky imaging (`internal/planetary`): sharpness-ranked best frames, sharpened.

The output `format` (`image`/`video`/`both`) additionally renders a Ken-Burns MP4 via
`internal/videoout` (ffmpeg).

## Tuning

Rejection thresholds (`internal/grade`, `Options`) and post-processing (`internal/postprocess`,
`Options`) have sensible defaults; both are passed through `pipeline.Options` for callers that want
to override them.
