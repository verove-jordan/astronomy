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
planetary lucky imaging the stack is **SNR-limited**: the frame keep rate is high (35% by default,
against planetary's 15%) and the sharpening works harder. On a capture whose frames score within a
few percent of each other, raising `keep_percent` buys depth more cheaply than selectivity buys
sharpness — the trade is worth making explicitly rather than assuming either way.

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
is recorded as an advisory and kept: a bracketed session is normal, and step 3b composites it.

**2 — Ingest** (`ingest.go`, `scan.go`, `still.go`). Video is two-pass: stream the clip once at
reduced resolution scoring every frame and writing nothing, then re-decode and materialise only the
winners, cropped to the disc. A 25 s 4K120 clip is 3 000 frames; extracting them all would be tens of
gigabytes. HLG and PQ are inverted in Go (the host ffmpeg has no `zscale`), limited range is expanded
explicitly, and the luma plane is taken rather than an RGB conversion — a phone clip is 4:2:0 or
4:2:2, so chroma is half or quarter resolution and an RGB average would dilute the signal.

The scan records two things per frame, not one: sharpness, and **transmission** — the on-disc median,
which is what a passing cloud moves. Frames below `transparency_floor` (95%) of the clip's clearest
are dropped *before* the sharpness ranking, and that ordering is the point. Sharpness here is
contrast measured over the frame's own median, so it sees a cloud's veiling glow but is blind to its
extinction, and it puts what it does see on the same axis as seeing — which is not comparable. A
frame blurred by seeing is a fair sample that registration and averaging improve; a frame behind
cloud is the Sun plus an additive glow that no amount of averaging removes. Worse, photometric
normalisation downstream maps its disc back onto the group median, scaling the veil up with the
signal so it arrives at the stack looking correctly exposed. The reference is the 90th percentile of
the clip's own levels — not the maximum, which is one frame, and not the median, which sinks with the
cloud. A clip clouded throughout keeps its clearest 40% and says so rather than returning nothing.

Raws are developed **without white balance and in raw camera colour** (`-r 1 1 1 1 -o 0`). Both
matter: the sRGB conversion matrix maps a monochromatic 656 nm source out of gamut and clips the red
channel across the whole disc, while the colour thumbnail still looks correctly exposed.

**3 — Split by exposure, then normalise** (`bracket.go`, `norm.go`). Files whose measured disc levels
differ by more than a stop are **exposure tiers**, and everything from here to the composite happens
per tier. Normalisation, windowing and sigma clipping all compare a frame against its siblings, and
across a bracket those comparisons are meaningless.

Within a tier, each frame's on-disc distribution is mapped onto the tier median through a monotone
piecewise-linear LUT. Not an affine fit: against a camera tone curve the residual is a function of
intensity, which on a limb-darkened disc is a function of radius — a limb that breathes frame to
frame, which the stack's clipping then eats. Under the fitted range the LUT extrapolates through the
**origin** rather than continuing the lowest segment's slope; the curve is measured on the disc, so
everything outside the limb is under-range, and an affine extrapolation there drove the sky to minus
eight percent of the disc — a floor sitting exactly where the prominences live.

**4 — Window** (`window.go`). The session is split into stretches short enough that the scene is
frozen — 60 s and 150 frames by default. Field rotation on an alt-az mount is about 0.33°/min near
the meridian, which is seven pixels of limb motion per minute at a 1 200 px radius. Windows below the
minimum are dropped rather than stacked differently.

**5 — Register & stack** (`register.go`, `regmodel.go`, `stack.go`). The transform is a similarity,
derived rather than searched: the fitted limb gives scale and translation. Rotation is measured
separately by correlating a mid-disc annulus, because a circle is rotation-invariant. All three
compose into one cubic resample. Then a two-pass sigma-clipped weighted mean, weighted gently by
sharpness.

Two of those three terms are then **regularised, not taken as measured** (`regmodel.go`). Translation
is genuinely per frame — the disc really does wander across the sensor. Scale and rotation are not:
within one clip the optics do not move and the Sun does not change size, so their per-frame scatter is
measurement error. Scale is pinned to a robust constant per source; rotation is fitted as a robust
line in time per source, which absorbs an alt-az field turning while it records and absorbs the step
between two clips shot minutes apart, without inheriting the correlator's occasional wrong peak.

That distinction earns its place because scale and rotation errors both displace a feature in
proportion to its RADIUS — nothing at disc centre, everything at the limb. Measured on a real clip the
fitted radius drifted 904.0 → 908.6 px in seventeen seconds (4.9 px of smear at 0.9 R), and the
rotation correlator, asked to match frames 2.5° from its reference, returned individual frames at
−6.4°.

**The annulus profile is high-passed before it is correlated** (`detrendAzimuth`, harmonics 0–2), and
that is not a refinement — without it a two-clip stack is derotated by the wrong angle. The etalon's
sweet spot and the eyepiece's vignette lay a large smooth gradient across the annulus, and it MOVES
when the phone is re-seated between clips, which is precisely when derotation matters. The correlation
then lines up the gradients instead of the plage, and does so confidently: measured across two real
clips it returned 1.62° ± 0.17° where the truth — from stacking each clip alone and correlating the
two masters — is 1.025°. Nine pixels of pure error at the limb. Stripping the first two harmonics
brings the per-frame estimate to 1.058°.

**A rotation error is invisible to every disc metric**, which is why it survived so long: rotating a
disc about its own centre maps the disc onto itself, so the limb lands exactly where it was and the
measured PSF is unchanged. Only the prominences move, and they come out doubled — rendering as dark
notches bitten out of the limb. The bench measures them separately (`promContrast`), thresholding on
the residual above the modelled off-limb background rather than on raw brightness, because the
scattered-light skirt is brighter than any prominence and is itself azimuthally uniform.

**The single most damaging default was the alignment-point field's measurement resolution.** It was
computed on a raster reduced 4×, on the reasoning that the distortion field varies over tens of
pixels. That confused the field's *scale* with its *amplitude*: after the limb fit has removed the
rigid part, what is left to measure is a few pixels, so at quarter scale the correlator was being
asked for a sub-pixel answer from a raster that could not represent one — and every frame was warped
by the noise it returned. Fine contrast at 0.9 R, as a fraction of disc centre:

| reduction 4 (old) | reduction 2 | reduction 1 (now) | no field at all |
|---|---|---|---|
| 43% | 57% | **85%** | 101% |

A single frame shows no fall-off at all, so all of that was manufactured. It is exactly the "sharp in
the middle, out of focus at the edges" that sends people looking for a collimation or field-curvature
problem they do not have. `ap_scale` re-opens the speed trade; `ap_align` turns the field off.

Full resolution is affordable because the field is measured **coarse-to-fine**. A single full-scale
search spends nearly all of its work discovering roughly where each node matches — a question a
half-scale raster answers perfectly well — so the first rung locates every node to about a pixel and
the second refines it at full scale over a two-pixel window: 25 offsets instead of 225. And the
correlator is handed a pre-blurred raster, because it blurs whatever it is given and it was being
given the whole frame once per alignment point — several megapixels re-blurred a few hundred times to
correlate patches a hundred pixels across. That single redundancy, not the search, was what made full
scale look inherently expensive. Together: **41 minutes → 6.9** on a 154-frame clip, keeping 82% of
centre contrast at 0.9 R against the exhaustive search's 87%.

**5b — Composite the bracket** (`bracket.go`). Each tier's sharpest window is registered onto the
brightest tier's raster (the field turns between clips shot a minute apart, so the rotation is solved
and then verified — one that does not improve the match is discarded), put on its scale through a
tonal mapping fitted from **paired pixels**, and combined by inverse variance.

Two details carry the whole thing. The mapping is fitted **on the disc only** and runs to the origin
below it: a solar raster is mostly sky, and a fit including it is dominated by pixels where both
exposures read their own noise, which returns a near-zero — or here, measurably inverted — slope at
the faint end. And each tier's noise is carried onto the reference scale through the **local slope**
of that mapping, floored at the exposure ratio. That single term replaces every mask: on the disc the
slope is the ratio, so a darker tier is simply the noisier estimate and contributes in proportion;
off the limb it is far steeper, so its noise explodes in reference units and the prominences come
from the exposure that actually recorded them. A session shot at one exposure produces one tier and
is byte-identical to a run that never knew about brackets.

**6 — Resolve the finish** (`psf.go`, `autotune.go`). Deconvolution width is the one setting here with
a true value rather than a tasteful one, and the limb hands it over for free: at this plate scale the
chromosphere's scale height is far below a pixel, so the true edge is a step and everything smearing
it is the system PSF. It is read off the **derivative** of the edge — reading crossings of the edge's
own height instead lets limb darkening in, which measures three times too wide.

Measured on real iPhone clips the answer lands between 0.8 and 1.6 px, against a former constant of
2.0. That gap is not a small error: a kernel wider than the truth does not blur, it over-corrects,
manufacturing texture at scales the optics never delivered while leaving the genuinely blurred band
uncorrected — noisy and soft at once. Starlet gains are capped below the resolved scale for the same
reason.

The same profile detects a **camera that sharpened the frames before we saw them**: an optical edge
cannot overshoot, a sharpened one leaves a bright shelf just inside the limb. Rather than keeping a
list of phone models — wrong the moment the firmware changes — the run measures the overshoot and
cuts the iteration count in proportion, and says so. `deconv_auto` off, or naming `deconv_sigma`,
restores a fixed width.

**7 — Finish** (`finish.go`). Order is the design:

```
instrument flat → Richardson-Lucy → starlet gains → limb-darkening flatten
                → disc glow → prominence composite → palette + tone
```

The flat runs per frame before registration (rings are sensor-fixed while the Sun drifts) and before
deconvolution (it corrupts the image, not the scene). The limb-darkening flatten runs *after* both
sharpening stages, because its gain rises fastest exactly at the limb and because RL's damping and
the starlet thresholds both need noise that is still stationary.

**The finished image never brightens on the way out across the limb.** The Sun has no ring around it;
the raw frames have none, so neither may we. That one rule is now measured rather than eyeballed
(`ringAmplitude`, the largest rise between 0.95 R and 1.05 R of the rendered radial profile) and it is
what the last three stages are built around:

- The **prominence composite** is gated strictly outside the limb and composites by taking whichever
  rendering is brighter, rather than crossfading. It used to feather across ±0.02 R — eighteen pixels
  either side at a 900 px radius — and the off-limb curve renders a disc-level pixel as pure white, so
  a bright band was painted exactly where the physical limb sits. `prominence_feather` now only sets
  the sharpening mask's transition.
- The **off-limb background model** samples radius in `t^0.75` rather than uniformly. The
  scattered-light skirt falls by an order of magnitude within a few pixels of the limb and by almost
  nothing over the next few hundred; uniform annuli average the whole transition into one bin,
  under-subtract it, and the prominence stretch renders the residual as a ring just outside the limb.
- The **disc glow** (`glow_strength`, `glow_radius`) is a deliberate, synthetic halo — the real aureole
  stays subtracted, because leaving it in makes every prominence render on a sloping background. It is
  anchored to what the limb itself renders at and composited by taking the brighter of the two, which
  makes the profile monotone by construction rather than by tuning.

Two long-standing radial errors were fixed alongside. `RadialProfile.rawGain` recovered its bin index
by *multiplying* by `ldFitLimit` where the binning divides, so every limb-darkening gain was read
3% too far in — a residual dark rim on every solar image, and the top seven bins never read at all.
`smoothProfile` left its end samples untouched, so on the two monotone profiles that use it the second
bin was smoothed against two raw neighbours and came out roughly double; it now continues each profile
past its ends by reflecting about the endpoint's *value*, which preserves a straight line exactly.

## Preset knobs & defaults

Tier A re-renders in seconds and is what the supervisor and the Refine panel tune:
`flat_strength` 0.6, `deconv_auto` on, `deconv_sigma` 2.0 (the fallback when the limb cannot be
measured; naming it turns `deconv_auto` off), `deconv_iters` 50,
`sharpen_small`/`_medium`/`_large`, `sharpen_denoise`, `limb_flatten` 0.85, `prominence_boost` 1.0,
`prominence_feather` 0.020, `palette` gold, `stretch` 0.5, `contrast` 1.0, `saturation` 1.0,
`background_level` 0.03, `background_tint` 1.0, `glow_strength` 1.0, `glow_radius` 0.05.

The **built-in presets name none of these deconvolution settings**, which is deliberate. Naming
`deconv_sigma` is how a user turns the measurement off, and every sun preset used to pin it at
1.2–1.5 px — tuned when that was the only number available, against captures that measure three to
four. Each preset therefore shipped a deliberately under-deconvolved rendering and none could ever
benefit from the measurement. What is left in them is the part that really is taste.

Tier C re-reads the frames: `band` auto, `keep_percent` 35, `max_frames` 300, `drizzle` 1,
`clip_sigma` 3, `window_seconds` 60, `window_frames` 150, `min_frames` 12, `crop_margin` 0.18,
`scale_tolerance` 0.03, `bracket_merge` on, `bracket_stops` 1.0, `transparency_floor` 0.95,
`ap_align` on, `ap_scale` 1.

## Soft-fail fallbacks

- No dcraw → `sips`, with a warning that white balance and the tone curve cannot be disabled.
- HLG/PQ unrecognised → treated as SDR.
- A clip that will not probe or decode → skipped with a warning; the run continues.
- Fewer than 8 frames with a fitted limb → the transparency gate stands down; there is nothing to
  establish what "clear" looked like.
- Limb unfittable on a frame → that frame is skipped, never guessed at.
- Limb unmeasurable on the master → deconvolution falls back to the fixed `deconv_sigma`, with a note.
- A tier that will not register or shares too little signal with the reference → left out of the
  composite, named in the warnings; the run finishes on the tiers that did combine.
- StarNet++ and GraXpert are **not** used: star removal is meaningless here and background
  extraction would eat limb darkening as a "gradient".

## AI supervisor

Tier A only, via `refineSun`, replayed over `master_hdr.fits` when the run composited a bracket and
`master_wNN.fits` otherwise — a re-finish has to replay the image the run actually produced, and it
resolves the deconvolution width the same way the run did.

## Outputs & artifacts

`<object>_stack.png` (16-bit), `master_wNN.fits` per window of the reference exposure,
`master_tNN_wNN.fits` for the other tiers, `master_hdr.fits` for the composite, `triage.json`, and
`<object>_timelapse.mp4` whenever the session split into more than one window — the evolution is the
result, not an optional extra. The time-lapse runs over the reference tier alone: a bracket is a
sequence in exposure, and interleaving it with a sequence in time reads as a flicker.

`run.json` carries `psf` (the resolved width, and whether the camera had pre-sharpened) and
`bracket` (per tier: stops below the reference, solved rotation, and the share of the composite it
contributed — the number that says whether the bracket was worth shooting).

## Config/env

`FFMPEG_BIN`, `DCRAW_BIN`, `SIPS_BIN`. No Siril, no plate solving, no calibration library.
