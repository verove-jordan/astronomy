# `eclipse` — a partially eclipsed Sun

Entry point: `pipeline.ProcessSun` (`internal/pipeline/sunmode.go`), reached with `mode.For(Eclipse)`.
Geometry and masking: `internal/solar/pair.go`, `pairmask.go`.

Eclipse is the [`sun`](sun.md) recipe run against **two circles instead of one**. It is a separate
mode rather than a knob because the second body does not add a step to the pipeline — it changes what
every existing measurement *means*. "The disc" is no longer the subject, "the limb" is two limbs, and
the brightest edge in the frame belongs to a body that moves while the Sun does not.

## Why one circle is not enough

The visible boundary of a partially eclipsed Sun lies on two circles of near-equal radius: the solar
limb outside, the lunar limb inside. Near maximum, roughly half the boundary points belong to each.
Handed that mixture, the algebraic fit in `limb.go` converges on a blend and its robust trim then
keeps whichever population happens to win — **which changes from frame to frame**. Everything
downstream is defined against that circle.

Measured on fixtures where an occulter crosses a fixed Sun, the recovered solar circle wandered
**42.35 px** with the one-body fit and **0.16 px** with the two-body one
(`TestFitPair_DoesNotFlipAsTheOccultationDeepens`).

## How the two bodies are told apart

Not by fitting two circles and labelling them afterwards — near maximum they sit only a crescent-width
apart (about 20 px against a 300 px radius on the 12 Aug 2026 clips), so a sample spanning both edges
still yields a plausible circle. They are separated **per boundary point, before any fitting**, by the
direction the brightness steps:

- the **solar** limb goes bright → dark walking outward (Sun, then sky)
- the **lunar** limb goes dark → bright (Moon, then the still-visible Sun)

Both gradients point *into* the crescent, which is exactly why they are opposite: the crescent lies
inside one edge and outside the other. Two samples either side of each point classify it outright.

A frame with no occulter classifies every point as solar and reports `Moon.R == 0`, so a full disc
goes through this code to the answer `FitLimb` would have given.

## What the occulter changes downstream

| Measurement | Without the fix |
|---|---|
| **The stack** (`solar.Stack`) | Averages Moon with Sun along the edge's path and renders a **grey ramp** instead of a step. Fixed by dropping the occulter's whole sweep from COVERAGE — see below. |
| **The transparency gate** (`discLevelPair`) | Reads the deepening eclipse as thickening cloud. On the 17-minute clip the clip-wide reference discarded 5400 of 9000 fixture frames; a local reference drops exactly the 600 that were behind cloud. |
| **Photometric normalisation** (`norm.go`) | Fits the LUT over a "disc" that is mostly Moon, so two frames minutes apart differ mostly in obscuration and the LUT stretches one onto the other — normalising the eclipse away. |
| **Sharpness ranking** (`FrameSharpnessPair`) | See below — the surprise. |

### The stack excludes the whole sweep, not each frame's own occulter

Masking each frame against its own Moon is the obvious choice and it is wrong in a way that looks
*better*: each pixel then averages only the frames in which it happened to be Sun, so the swept band
fills in at full brightness from a dwindling number of frames, ending in a hard cut. That renders an
edge **sharper than the optics can produce** (measured 0.80 px against a single frame's 3.21) a few
pixels from where the Moon actually was, with a noise gradient running up to it.

So the window's entire sweep is excluded. Every surviving pixel is backed by every frame — uniform
depth, uniform noise — and the band itself comes from a second stack registered on the Moon.

### A second stack, registered on the occulter

The sun anchor's hole is not small. It is the occulter dilated by half the drift plus the guard —
about six canonical pixels — and on a crescent twenty pixels across near maximum that is a third of
the visible Sun, at exactly the edge the eye goes to. Left alone the finish invents it from the
disc's radial model: smooth, plausible, and carrying no filament, no plage and no granulation,
because a radial median has none by construction.

So the same frames are stacked a second time with the *occulter* held still (`pairstack.go`). The two
anchors differ by a translation and nothing else — scale and rotation belong to the optics and the
mount, so they are taken unchanged from the sun-anchored solution and only the frame's own occulter
is slid onto the window's mid-point. The occulter is then stationary by construction, so every frame
contributes to every pixel of the band, while the Sun — which now moves — is smeared by at most half
the drift. That is a good trade precisely where the sun anchor has nothing: 2.5 px of smear against
6 px of invention.

Measured against ground truth (the same Sun drawn with nothing in front of it), the recovered band is
**7.7× closer than the radial model** — RMS 0.035 against 0.266. And the occulter's edge lands at
**3.35 px against a single frame's 3.21**, so it is the optics' edge rather than the motion's
(8.39 px naive) or a coverage boundary rendered as a limb (0.80 px, which is what the sun anchor
alone produced).

The join is made by **blurring the sun anchor's coverage mask**, not by a distance from any circle.
The swept region is the occulter's disc dragged along its direction of travel — a stadium, not a
disc — so a radial crossfade would be too wide across the motion and too narrow along it, leaving a
lens of one master's noise on two sides.

What this does *not* buy at these plate scales is the Moon's own limb detail. Lunar limb mountains run
3–4 km, which at 384,000 km is ~1.6″ — half a pixel at 3.14″/px. The edge is a smooth circle here
whatever we do. The band of real Sun beside it is the whole point.

### The occulter's edge is KEPT in the sharpness metric

Masking the Moon out of the frame ranking looks obviously right and measurement reversed it. The
lunar limb is an opaque body against the Sun — a true knife edge, with no limb darkening, no
chromospheric skirt and no prominences — so it is the cleanest probe of the system's blur anywhere in
the frame. Keeping it separates a frame drawn at σ 1.0 from one at σ 2.2 by a factor of ~2.1 at every
obscuration; masking it drops that to 1.35, and on a thin crescent to **0.92 — an inversion**, which
would have selection keep the blurriest frames in the clip.

What *is* masked is the brightness the score is divided by. That must come from the visible Sun alone
or it falls with obscuration, inflating every frame's score — and selection ranks a whole clip at once.

### The correlation refiner is switched off

`refine.go` maximises whole-disc agreement against a reference. On an eclipse the occulter's edge is
one of the strongest features in the frame and the only one that *moves*, so the best whole-image
match is partly the Moon's motion applied to the Sun: a beautifully sharp lunar edge on a smeared Sun,
which every whole-image metric reports as an improvement. Measured, it collapses a 10 px sweep to
3.78 px (`TestStack_TheRefinerRegistersOnTheMoon`). The two-body fit is precise enough to stand alone.

## Preset

Derived from `Sun` (`mode.presetFor`), overriding only what the geometry demands:

| Knob | Sun | Eclipse | Why |
|---|---|---|---|
| `two_body` | off | **on** | the whole point |
| `window_seconds` | 60 | **30** | the Moon moves 0.508″/s — 9.8 px/min at 3.1″/px, so 60 s smears its edge by 10 px against a ~2 px PSF |
| `window_frames` / `max_frames` | 150 / 300 | **1200** | 30 s at 30 fps is 900 frames; the solar caps would split every window into six |
| `keep_percent` | 35 | **70** | diffraction-limited, not seeing-limited: frames vary little and the stack is SNR-limited |
| `drizzle` | 1 | **1.5** | ~2.5× undersampled (3.1″/px against a ~2.3″ diffraction FWHM), with ample sub-pixel dither |
| `palette` | gold | **native** | the crescent has a colour the phone recorded; see below |

`two_body` is also a knob in its own right, so a solar run can be told to look for an occulter and an
eclipse run can be told not to.

Everything else, including the whole finish surface, is the solar preset — so the Refine panel and
the knob menu are the same.

## Running it

```
curl -sS -XPOST localhost:8080/api/jobs -H 'content-type: application/json' \
  -d '{"path":"input/Iphone_eclipse_12_08_2026/08122026202918901.MOV","mode":"eclipse","format":"image"}'
```

Point it at one clip or at a folder; triage groups a folder by measured disc size as usual.

For the phase sequence, point it at the whole session and ask for panels:

```
curl -sS -XPOST localhost:8080/api/jobs -H 'content-type: application/json' \
  -d '{"path":"input/Iphone_eclipse_12_08_2026","mode":"eclipse","format":"image",
       "params":{"sequence_panels":11,"max_frames":2000}}'
```

`sequence_panels` implies `rescale_groups`: the clips of an eclipse are shot at whatever
magnification the phone happened to be at, and left in separate scale groups only one of them would
ever reach the sheet.

## The finish sees a whole Sun

Six measurements inside the finish read "the disc" — the instrument flat, the deconvolution, the
limb-darkening profile, the tone curve's anchor, the off-limb halo model and the prominence
reference. All six are radial or azimuthal averages, and the occulted region arrives EMPTY because
the stack excluded it, so all six would average zeros in.

Rather than six masked variants, the hole is **filled** before the finish with the disc's own radial
median measured from the visible Sun, and **painted back** to the background level after it
(`pairfinish.go`). The fill is legitimate, not a trick: limb darkening is a function of radius and the
Sun is radially symmetric, so the brightness under the Moon really is the brightness measured at that
radius elsewhere. It also removes Richardson-Lucy's worst artefact for free — the hole is a step
larger than the solar limb's, sitting *inside* the disc where `extendDisc`'s protection does not
reach, and RL rings on it.

Measured on a 40%-obscuration fixture: the crescent renders at **0.66 without the fill against a true
0.50 — a third too bright** — and 0.40 with it. The occulter itself lands at 0.0312 against a sky of
0.0311, so it reads as a body silhouetted against the sky rather than a hole punched in the picture.

## A filament looks exactly like the Moon

Geometry alone cannot tell an occulting body from a dark **filament**. A filament dips below the mask
threshold, so its outline becomes boundary points — and every one of them steps dark-inside to
bright-outside, exactly as the lunar limb does. Two guards, both needed:

- **Consensus before least squares** (`fitRobust`). A least-squares circle cannot defend itself: every
  point pulls on it, and the robust trim that follows measures deviations about a circle already
  dragged away. Measured at 40% obscuration, **17 spurious points out of 436** moved the fitted radius
  from 1.03 of the Sun's to 0.75 and its residual from 0.31 px to 39.92.
- **An opacity gate.** The Moon passes no light; a filament is only about half a magnitude down. The
  fitted circle's interior is measured and refused unless it sits near the sky's own level.

## Colour comes from the recording

`palette: native` measures the capture's own colour off the source clip instead of choosing one
(`native.go`). Everything else in this package is monochrome for good reasons — the etalon passes
0.6 Å and the ingest takes the luma plane because 4:2:2 chroma is half-resolution — but all of that
is about DETAIL and says nothing about hue. The light still lands on a colour filter array and comes
back through the phone's rendering as a particular orange, and that orange is what the capture
looked like. Measured on the 12 Aug clips, the bright part of the frame is **(0.59, 0.16, 0.15)**.

Three decisions, each load-bearing:

- **Indexed by quantile, not by brightness.** Between the recording and the finished image sit a
  deconvolution, a starlet pass, a limb-darkening correction and a strongly non-linear tone curve.
  Rank is the one thing that whole chain preserves, so the darkest tenth of the recording is still
  the darkest tenth of the render and the colour measured there belongs there.
- **Display-referred, not linearised** — the opposite of everything else here. "The colour of the
  recording" means what the clip looks like when played, and the ramp is applied to the finish's
  output, which is display-referred too; linearising would put a transfer curve between the two for
  nothing. It is also the only safe choice, because the `gray16be` and `rgb48be` decode paths were
  measured NOT to agree on scale, and chromaticity normalised to unit luminance is invariant to that.
- **Sampled around the disc.** The Sun covers a few percent of a phone's frame, so quantiles taken
  over the whole thing would be sky nine times in ten.

Brightness stays entirely the finish's business — the measurement carries hue and saturation only —
so the exposure and stretch you have already tuned are untouched. Highlights desaturate rather than
clip (measured roll-off: saturation 0.98 → 0.73 → 0.44 → 0.15 as luminance rises), because clipping
each channel independently would swing the hue towards yellow on plage and flares specifically.

## The stack is currently blurrier than its own frames

Measured on the 12 Aug clip, in the frames' own pixels:

| | sigma (px) |
|---|---|
| single frame, occulter's knife edge | **1.09** (best 1.01) |
| single frame, solar limb | 1.72 |
| one frame through the warp, alone | 1.66 |
| the 834-frame master | **2.30** |

The capture resolved 1.09 and the stack delivered 2.30. On the canonical raster the stack alone costs
2.49 → 3.45, a 39% increase. This is the same failure `sun-stack-registration-loss` documents for
full-disc solar, and it means **more frames cannot help** — the loss is registration.

The refiner is now occulter-aware (`comet.AlignSeededMasked`) and that was worth doing — on fixtures
it goes from smearing the Sun by 54% to being indistinguishable from no refinement at all — but it
was **not** the main loss. Measured on 160 contiguous real frames, solar limb sigma:

| config | sigma |
|---|---|
| AP field off, refinement on | **3.11** |
| derotation off | 3.16 |
| AP field on | 3.17 |
| refinement off | 3.25 |

Derotation contributes nothing: its annulus sits at 0.70 R, which on this master is *entirely* inside
the occulter, so its estimates are noise the robust model discards. The AP field costs 2% — its nodes
correlate Moon against Moon — and is off for eclipse. The masked refinement gains 4%.

What remains is geometry rather than a defect. A single frame is 2.58 canonical against the stack's
3.11, implying ~1.7 canonical pixels of per-frame centring scatter: a circle fitted to a CRESCENT is
constrained by a short arc, so its centre is poorly determined perpendicular to it, and the refiner's
correlation has only that same thin crescent to work with.

Until then, `best_frames` (default 12, at least 20 s apart) exports the sharpest individual frames as
finished images alongside the stack, because on this capture the best single frame is a genuine
candidate for the best picture. They are finished against their OWN geometry and their own point
spread function — a frame is not a small stack, and deconvolving one at the master's width would blur
the sharp ones and over-correct the soft ones.

## The phase sequence

`sequence_panels` renders the progression poster — the whole eclipse on one sheet, phases stepping
from a shallow bite through maximum and back out. `internal/eclipsegeom` chooses the phases,
`solar/panel.go` brings them into one sky frame, `solar/seqcanvas.go` lays them out, and
`pipeline/sunsequence.go` runs it.

### The phases come from the sky, not from the picture

The two-circle fit is at its weakest exactly where a sequence needs it most. At 96 % obscuration the
solar arc is a sliver twenty pixels wide, so the fitted centre is poorly determined perpendicular to
it; near contact the bite spans so little of the limb that `fitMoon` may decline it altogether. So
the ladder is built from the ephemeris (`eclipsegeom.At`, topocentric — the Moon's parallax is nearly
a degree, four solar radii, so a geocentric answer is a different eclipse), and the measured
obscuration is recorded beside the predicted one as a check. They are expected to agree; a run warns
when a panel's two numbers differ by more than five points, and names the fit as the suspect.

The site comes from the clips themselves: an iPhone writes
`com.apple.quicktime.location.ISO6709` into every recording, read in `solar/video.go`. `site_lat` /
`site_lon` override it, and without either there is no sequence — a phase cannot be computed without
a place.

Validated against two published eclipses rather than against itself: at Madras, Oregon on 21 Aug 2017
the obscuration reaches exactly 1.0 at 17:20 UTC (centre line), and at Seattle the same eclipse peaks
at 0.920 (published ≈ 0.92) — `TestMaximum_Totality2017`, `TestMaximum_OutsideThePath2017`.

### Nothing is mirrored, so the ladder decides how many panels there are

`sequence_panels` is a request. A rung is placed only where BOTH sides of maximum can supply a phase
within `magMatchTol` (0.12 of the diameter) of each other, so the two halves mirror each other
without anything being reflected or interpolated. Where the recording has a hole, the ladder places
fewer rungs and says so.

The 12 Aug 2026 session is the worked example. Coverage, computed from the clips' own timestamps:

| clip | UTC | obscuration | Sun alt |
|---|---|---|---|
| `…193801119` | 17:30:06 → 17:30:21 | 3.6 → 3.8 % | 18.4° |
| `…200756943` | 17:47:51 → 17:49:28 | 29.9 → 33.0 % | 15.4° |
| `…202918901` | 18:11:49 → 18:29:18 | 80.8 → **96.3** → 81.7 % | 11.3 → 8.4° |
| `…204454646` | 18:39:54 → 18:44:54 | 57.3 → 46.1 % | 6.7 → 5.9° |
| `…205407129` | 18:46:40 → 18:54:06 | 42.3 → 26.9 % | 5.6 → 4.4° |
| `…211359168` | 19:12:33 → 19:13:58 | 0.4 → 0.0 % | 1.4 → 1.2° |

First contact 17:24:37, maximum 18:20:48 at 96.27 %, last contact 19:13:44 — the eclipse ended
fourteen seconds before the last clip did, and five minutes before sunset. Twenty-two minutes of the
ingress (33 % → 81 %) were not recorded, which is why asking for eleven panels yields nine:
3.6 / 33 / 81 / 90.5 / **96.3** / 90.5 / 81.7 / 46.3 / 0.4 %, mean pair mismatch 0.05 of the diameter
(`TestPlanLadder_TheSession`).

Rungs are spaced evenly in log(remaining crescent thickness), not in obscuration. Obscuration is an
area and it saturates: between 90 % and 96 % covered the crescent goes from a tenth of the radius
thick to a twenty-fifth — a visibly different picture — while the area moves six points.

### The roll comes from comparing the sky with the picture

The panels come from different clips of a hand-held afocal rig, so each arrives at its own scale, its
own roll and possibly its own handedness. Over this session the Moon swings 175° around the Sun, so
this is not a refinement — skipping it gives a scatter of crescents rather than an eclipse.

The true position angle of the Moon at each instant is known, so subtracting the one measured in the
picture gives the camera's roll directly, with no plate solving. Handedness is solved ONCE for the
whole set and recovered from the sweep alone: a mirrored image runs the swing backwards, so the wrong
parity makes a "constant" camera roll rotate by twice the swing. Measured on fixtures, roll and parity
come back exactly (`TestSolveOrientation_RecoversRollAndParity`), and end to end the occulters land
within 0.2° of their true position angles (`TestSequenceGlue_BringsEveryPhaseIntoOneSkyFrame`).

A panel whose occulter cannot be fitted — a shallow bite near contact — borrows the roll of its
nearest neighbour, preferring the same clip. Zero would be the easy answer and the worst one: roll
belongs to the camera, so a neighbour from the same seating of the phone is right to within degrees,
while unrotated is wrong by however the phone happened to be held.

Below 8° altitude the disc is also stretched back out along the local vertical, which the parallactic
angle locates. At 1.3° — where the last clip sits — refraction compresses it about 5 %, which is a
visibly oval Sun standing next to eight round ones.

### A panel is ONE frame, not a stack

A stack of a crescent pays for registration twice, and the solar arc is short, so the fitted centre
is poorly constrained perpendicular to it and that scatter reaches every pixel. Measured on these
clips the 834-frame master resolves the occulter's edge at σ 2.30 px against **1.09 for a single
frame** — and rendered it is not merely softer: the band the occulter's sweep took out is recovered
by a second, Moon-anchored stack and joins along a **hard dark arc through the crescent**, the cusps
double, and sharpening turns the averaged noise into a mottled crust.

So a panel is one frame, placed by one cubic resample. `sequence_stack` restores the two-candidate
comparison for a capture where the trade goes the other way.

### Choosing that frame

Four things decide it, and each exists because the obvious version failed on real frames.

**Bounded by PHASE, not by clock.** A thirty-second window is a *stacking* window — it bounds how far
the Moon smears while frames are averaged — and using it to choose one frame throws away thousands of
equally valid candidates. Candidates are instead every frame whose computed obscuration is within
`panelPhaseTol` (3 points; 1 point for the centre) of the panel's own, capped at a few minutes. This
widens the search on its own exactly where the phase changes slowly. Bounding by time instead let the
centre panel drift five minutes off maximum chasing a sharper frame and come back **an 82 % crescent
labelled 96 %**.

**Ranked by the SOLAR limb, over every candidate.** Two metrics ranked this before and both were
measuring something other than resolution. `FrameSharpness` is band-pass energy over the frame's own
level, and codec block noise lives in the same band — on-disc video noise has been measured at
hundreds of times the sky estimate it normalises against — so it flatters the noisiest frames.
`MeasureSharpness` then looked right and was worse: it prefers the **occulter's** knife edge, which
beats the solar limb against a synthetic blur, and on the real clips it is noise. Over the 82 %
stretch of 12 Aug 2026 both probes had thirty-odd usable wedges on every frame and came out
*anti-correlated* — the frame the occulter liked best (0.94 px) has a solar limb of 1.78 px / 13.7 ″,
while the band's sharpest frame (1.11 px / 9.1 ″) was ranked **worst of all** at 1.19. The occulter's
whole range across the band was 0.94–1.40 against the limb's 1.06–1.86: the lunar limb sits on the
codec-crushed dark side of the crescent, so its width is set by deblocking ringing and barely moves
with the seeing.

That is how the 81 % panel came to be the softest of the seven while its own note claimed 0.83 px. It
was not soft material chosen carefully — it was sharp material passed over. Selection now measures
`MeasurePSF` on the solar limb of **every** candidate, which is also what the finished panel is graded
on (the placement masks the occulter out, so `MeasureSharpness` finds too few wedges there and drops
through to the limb by itself). Selection and grading finally ask the same question. It costs less
than the old shortlist, not more: the containment test already reads every candidate's raster and the
limb measurement rides along on that read.

**Widened per rung when a rung is starved.** One tolerance assumes an even recording and an eclipse
recording never is. Cloud empties minutes of it, and ingest's per-clip `max_frames` cap spends its
whole budget wherever the clip was clearest — on 12 Aug 2026 that put 1468 of one clip's 2000
materialised frames into its first 1.2 minutes and left the next fourteen with 31. The same tolerance
therefore caught 1474 candidates for one rung and 12 for another. A non-peak rung holding fewer than
`panelWantCandidates` widens in steps to `panelMaxPhaseTol` (6 points) and says so. **The peak rung
never widens** — its entire claim is "this is as deep as it got".

**Widening stays inside the rung's own clips**, and that restriction is what makes it safe rather than
merely bigger. A clip is one exposure, one magnification and one pass through the phone's encoder, and
none of those match between clips of a hand-held session. Letting panel 6 widen freely reached from
its own clip into one recorded at a **smaller image scale** — 270 px of solar radius against 293 — and
far more heavily compressed: its disc is quantised into flat patches with no granulation left. And
quantisation *snaps* the limb gradient into a step, so that frame measured **0.87 px against the
honest frame's 1.29** and won on every metric while being plainly the worse picture. No sharpness
measure can see that difference. The clip boundary can.

Ranking is also on **FWHM in arcseconds**, not σ in pixels. Pixels are only comparable at one image
scale, so across clips a pixel width silently flatters whichever was least magnified.

**Refused if the disc is not whole.** A Sun with a chord sliced off is wrong in a way no resolution
answers. The test is not `Limb.Partial` — that reports the disc running past the raster, and ingest
crops tightly by design, so it fires on most frames of most clips and once rejected 1216 of 1474
candidates for a panel whose result was fine. Nor is it the circle against the raster bounds: the
extraction pads its square, so a disc chopped by the *scan* crop sits comfortably inside a black
border. What is measured is whether there is **light just inside the fitted limb, all the way round**,
skipping the azimuths behind the occulter so a 96 % crescent does not read as 4 % present.

**And re-extracted when no frame can be whole.** The crop is one size for a whole clip, so if the Sun
is bigger than it, every frame has the same chords sliced off and selection cannot help. A rebuild
detects that per clip and re-cuts only those clips from the video, sized from the clip's **own**
measured disc rather than the merged group's — which is what went wrong to begin with, a session
whose magnification changed between clips having one group radius and several real ones. Re-cutting
sixteen seconds costs seconds where the session cost sixteen hours.

### What the finish must not do to a single frame

The solar finish is tuned for a stack, and three of its defaults are actively wrong on one frame:

| | default | on a sheet panel | why |
|---|---|---|---|
| `DeconvIters` | 50 | **12** | the default is sized for a master carrying ~1/17 of one frame's noise; at that depth on one frame it converts grain into ringing — the rough dark edge inside the crescent |
| `GlowStrength` | 1.0 | **0** | `addDiscGlow` paints an aureole because a bare stacked disc looks unfinished. This capture HAS one, so it drew a second — a brown arc around every crescent that is in none of the source frames |
| `LimbFlatten` | 0.85 | **0.30** | it measures a radial profile over a crescent spanning a few degrees of azimuth, and the natural falloff is part of what makes the raw frames look like the Sun |

The finest two starlet scales are also switched off. At 3.14 ″/px against a ~2.3 ″ diffraction FWHM
they sit **entirely below what the optics resolve** — there is no solar structure there, only the
codec's. Raising thresholds does not reach it, because they are set from noise measured on the sky.

**The off-limb ring is cut.** The instrument puts a diffraction and internal-reflection ring just
outside the limb — visible in the source frames, dark at some phases and bright at others, and drawn
once per panel on a sheet. Everything past 1.025 R is faded out (scaled by 1/flatten so the
refraction stretch is not clipped). The chromosphere picture does **not** go through this: prominences
live in exactly the band it discards.

### The chromosphere picture

`<object>_prominences_<palette>.png` is the deepest phase on its own, rendered for what stands off the
limb rather than for the disc: the tone curve pulled down so the crescent renders deep, the off-limb
lifted until the flames sit at the level of the arc they came from.

Its frame is chosen by looking for what the picture is OF. Sharpness cannot answer that — a
prominence is a property of the MOMENT, out for a few minutes on one part of the limb, and the
sharpest frame at maximum showed none at all. `solar.ProminenceSignal` measures the light in the
annulus just outside the solar limb, **per sector**: a flame is localised, and a mean around the whole
ring divides it by the ninety percent that is empty sky, which is how a frame with an obvious flame
scores the same as one without. The search finds the moment, then the edge picks the frame of it.

Two traps cost a render each. Deep in the eclipse the Moon is the **larger** disc and nearly
concentric, so almost the whole annulus is behind it and a flame is only visible within a few degrees
of the cusps — thresholds sized for a full disc rejected every frame. And the standard disc-level
normaliser returns exactly **zero** on a thin crescent (the mask is eroded from a crescent narrower
than the erosion), which silently made every deep frame score "no chromosphere".

Two more cost a render each, and both are about the picture being judged as though it were a panel.
**The whole-disc test does not apply to it.** Its subject is what stands *off* the limb at the deepest
moment, so the disc is supposed to be nearly gone; asking how much of it is present is a phase
measurement wearing a quality test's clothes. Applied here it reported **0.0 % of the disc present**
on every candidate at 85 % obscuration and warned about cloud that was not there. `wholeDiscPolicy`
now says which of the two questions is being asked.

**Its candidates have to be hydrated first.** A rebuild carries only paths and clocks, so a candidate
arrives with no fitted circles and every question asked of it downstream answers from zeroes: it
measures its own obscuration as 0 and trips the "no frame matched the sky" fallback, its limb cannot
be measured so the choice drops back to the band-pass score, and the containment probe reads 0.0 %
present and reports clear sky as cloud. All three fired on the 12 Aug 2026 sheet and all three were
this one omission — `composeSequence` held the hydrator and did not pass it on.

**And the moment window is a count, not just a clock.** Once the moment is known the frame is picked
the ordinary way, from a window around it — but `max_frames` caps a clip at a couple of thousand
materialised frames however long it runs, so a seventeen-minute clip reaches the sequence at under two
frames a second rather than thirty. Six seconds of that is **three pictures**, and choosing the
sharpest of three is barely choosing. The window is now 20 s with a floor of `promMomentCandidates`
under it, widening to the nearest candidates when the clock alone would not find enough.

The sheet's centre stays the true maximum regardless. The ladder's claim is that the panels either
side of it are matched phases; moving the centre to whichever minute had a flame out would break it.

### Output

```
<object>_sequence_native.png    the recording's own colour
<object>_sequence_gold.png      the poster convention
<object>_sequence_mono.png      prominences with nothing in the way
sequence/panel_NN.fits          the chosen frame, so a re-layout needs no re-extract
sequence/sequence.json          per panel: plan, ephemeris, measurement, orientation, both PSFs
previews/950_sequence.png
```

All three palettes are rendered because re-rendering a persisted panel costs a second where
re-stacking it costs minutes. Every panel of every sheet goes through ONE set of finish options, with
only the deconvolution width resolved per panel from its own limb: the crescent's surface brightness
does not fall as the Moon covers it, only the total light does, so a panel that set its own stretch
would render the deep phases brighter than the shallow ones.

The sheets composite by **max**. Every panel is a disc on black sky, so wherever two overlap one of
them is sky — no seams, no feathering, no visible lozenge where the square rasters cross.

The Refine panel lays the sheet out again from `sequence/panel_NN.fits` in seconds
(`pipeline/sunseqrefine.go`), so angle, spacing and palette iterate without going near the video.

When a run has no panels yet — it was stopped, or it predates the feature — the same refine
**rebuilds them from the run's own extracted frames** (`pipeline/sunseqrebuild.go`). That matters
more than it sounds: extracting 60 000 frames of 4K ProRes took sixteen hours on this session, where
everything downstream is minutes. `<work>/sun_<runID>/` plus the run's `triage.json` is enough to
reconstruct the exact frame list, because a video frame's clock is its container's creation time plus
its own index over the frame rate — the same arithmetic ingest used, not a guess. Geometry and
sharpness are measured on demand, for the few hundred frames the panels actually consider rather than
all eight thousand.

## Known gaps

- **Optical ghosts are not detected.** The 12 Aug clips show internal reflections — a faint arc inside
  the crescent and a detached bead off the lower cusp — and `blendProminences` boosts everything
  outside the solar limb, so it would render them as real prominences. A ghost moves with the FIELD
  rather than with the Sun, which is what a detector would key on. On a sequence the same error is
  drawn once per panel.
- **The sequence carries no labels.** The phase table is in `sequence.json`; drawing times and
  percentages onto the sheet would need a text renderer, and the only font available in Go here is a
  7 px bitmap that would have to be scaled twenty times.
- **The full-resolution sheet is large.** Nine panels at a 460 px solar radius is roughly
  10 000 × 4 800 px per palette. `PlanSequenceCanvas` shrinks the PANELS rather than resampling a
  finished sheet, so the cap costs resolution rather than a second interpolation.
