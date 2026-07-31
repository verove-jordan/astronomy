# sun

## What & when

The Sun, in Hα or white light. Built for an **Acuter Elite Phoenix 40** (40 mm, 400 mm f/10, front
etalon ≤0.6 Å at 656.3 nm) shot either with an iPhone through the supplied adapter or with the
ASI 1600MM Pro, but nothing in it is specific to that scope.

Solar imaging has none of the anchors the other modes rely on. There are no stars to register
against and no sky background to model, and the subject is a limb-darkened disc whose brightness
profile is the very thing that has to be removed before any detail is visible. What it does have is
a perfect geometric reference: the limb. Everything here is built on measuring it.

At 40 mm the resolution limit is the aperture (4.13″ Rayleigh at 656 nm), not the seeing, so unlike
planetary lucky imaging the stack is **SNR-limited**: the frame keep rate is high (65% by default,
against planetary's 15%) and the sharpening works harder.

## Detection & inputs

Point the run at the whole capture folder. A real session is a pile of attempts — different zooms,
resolutions, exposures and formats — so the run **triages** before it stacks.

| Input | Handling |
|---|---|
| iPhone `.mov` (HEVC / ProRes, 8- or 10-bit, HLG or SDR) | streamed, scored, best frames extracted |
| iPhone ProRAW `.dng`, `.heic` | developed narrowband-linear, channel chosen per group |
| FITS / TIFF / PNG / JPEG series | used directly |
| `.ser`, `.avi`, `.mp4`, `.mkv` | as video |

## Algorithm, end to end

**1 — Triage** (`internal/solar/triage.go`). Every file is probed: metadata, and a measured disc
(sub-pixel centre, radius, arc coverage, saturation, sharpness, limb-darkening shape). Files are then
grouped by **measured disc radius**, never by metadata.

That distinction is the whole point. Metadata lies about scale in both directions: a 48 MP frame at
24 mm and a 12 MP frame at 55 mm of digital zoom can land within 2% of the same disc diameter, while
two frames sharing both can differ because the phone was re-seated on the eyepiece. Groups are cut
where the sorted radii actually show a gap, with the total spread capped so a near-uniform ramp
cannot chain from one configuration into the next. The report is written to `triage.json` beside the
result, and `exclude_sets` drops groups by ID.

Frames are then gated. Absolute rejections where physics gives a threshold — a blown disc, a frame
far too dark, a limb too short to constrain a circle. Everything else, notably a different exposure,
is recorded as an advisory and kept: a bracketed session is normal, and normalisation handles it.

**2 — Ingest** (`ingest.go`, `scan.go`, `still.go`). Video is two-pass: stream the clip once at
reduced resolution scoring every frame and writing nothing, then re-decode and materialise only the
winners, cropped to the disc. A 25 s 4K120 clip is 3 000 frames; extracting them all would be tens of
gigabytes. HLG and PQ are inverted in Go (the host ffmpeg has no `zscale`), limited range is expanded
explicitly, and the luma plane is taken rather than an RGB conversion — a phone clip is 4:2:0 or
4:2:2, so chroma is half or quarter resolution and an RGB average would dilute the signal.

Raws are developed **without white balance and in raw camera colour** (`-r 1 1 1 1 -o 0`). Both
matter: the sRGB conversion matrix maps a monochromatic 656 nm source out of gamut and clips the red
channel across the whole disc, while the colour thumbnail still looks correctly exposed.

**3 — Normalise** (`norm.go`). Each frame's on-disc distribution is mapped onto the group median
through a monotone piecewise-linear LUT. Not an affine fit: against a camera tone curve the residual
is a function of intensity, which on a limb-darkened disc is a function of radius — a limb that
breathes frame to frame, which the stack's clipping then eats.

**4 — Window** (`window.go`). The session is split into stretches short enough that the scene is
frozen — 60 s and 150 frames by default. Field rotation on an alt-az mount is about 0.33°/min near
the meridian, which is seven pixels of limb motion per minute at a 1 200 px radius. Windows below the
minimum are dropped rather than stacked differently.

**5 — Register & stack** (`register.go`, `stack.go`). The transform is a similarity, derived rather
than searched: the fitted limb gives scale and translation exactly. Rotation is measured separately
by correlating a mid-disc annulus, because a circle is rotation-invariant. All three compose into
one cubic resample. Then a two-pass sigma-clipped weighted mean, weighted gently by sharpness.

**6 — Finish** (`finish.go`). Order is the design:

```
instrument flat → Richardson-Lucy → starlet gains → limb-darkening flatten
                → prominence composite → palette + tone
```

The flat runs per frame before registration (rings are sensor-fixed while the Sun drifts) and before
deconvolution (it corrupts the image, not the scene). The limb-darkening flatten runs *after* both
sharpening stages, because its gain rises fastest exactly at the limb and because RL's damping and
the starlet thresholds both need noise that is still stationary.

## Preset knobs & defaults

Tier A re-renders in seconds and is what the supervisor and the Refine panel tune:
`flat_strength` 0.6, `deconv_sigma` 1.4, `deconv_iters` 12, `sharpen_small`/`_medium`/`_large`,
`sharpen_denoise`, `limb_flatten` 0.85, `prominence_boost` 1.0, `prominence_feather` 0.006,
`palette` gold, `stretch` 0.5, `contrast` 1.0, `saturation` 1.0.

Tier C re-reads the frames: `band` auto, `keep_percent` 65, `max_frames` 300, `drizzle` 1,
`clip_sigma` 3, `window_seconds` 60, `window_frames` 150, `min_frames` 12, `crop_margin` 0.18,
`scale_tolerance` 0.03.

## Soft-fail fallbacks

- No dcraw → `sips`, with a warning that white balance and the tone curve cannot be disabled.
- HLG/PQ unrecognised → treated as SDR.
- A clip that will not probe or decode → skipped with a warning; the run continues.
- Limb unfittable on a frame → that frame is skipped, never guessed at.
- StarNet++ and GraXpert are **not** used: star removal is meaningless here and background
  extraction would eat limb darkening as a "gradient".

## AI supervisor

Tier A only, via `refineSun`, replayed over the persisted `master_wNN.fits`.

## Outputs & artifacts

`<object>_stack.png` (16-bit), `master_wNN.fits` per window, `triage.json`, and
`<object>_timelapse.mp4` whenever the session split into more than one window — the evolution is the
result, not an optional extra.

## Config/env

`FFMPEG_BIN`, `DCRAW_BIN`, `SIPS_BIN`. No Siril, no plate solving, no calibration library.
