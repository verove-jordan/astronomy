# Nightpano mode (sky panorama)

A Milky Way arch is wider than any camera field, so it is shot as a **hand-swept sequence of
pointings** across the sky and assembled into one image. Mode id: `nightpano`. Entry point:
`pipeline.ProcessNightpano` (`internal/pipeline/nightpano.go`); the geometry lives in
`internal/skypano`, the panel segmentation in `internal/panelgroup`.

> Not to be confused with mode `mosaic`. That one projects onto a gnomonic (TAN) tangent plane,
> which is **undefined 90° from its centre** — an arch is not distorted in TAN, it is
> unrepresentable — and it assembles one mono plane at a time. Nightpano is spherical and colour.

Nightpano **is the `milkyway` recipe run N times and then joined.** Every pointing stacks through
`nightscape.Process` with the same registration, calibration and clean-sky stack a single-pointing
milkyway run uses. What this mode adds is everything that only exists once there is more than one
pointing.

## The end-to-end flow

1. **Capture**: point, shoot a set, move, repeat. No folder structure is needed — see below.
2. **Group**: each frame's pointing is read from its own metadata (`internal/rawmeta` →
   `internal/pointing`) and the session is segmented into panels by how far the camera moved
   between consecutive frames (`internal/panelgroup`).
3. **Stack**: one nightscape stack per panel, sequentially, each with its own progress step and
   preview.
4. **Solve**: every panel is plate-solved by `skypano.AutoSolve` — quad/asterism hashing against
   the ATHYG deep catalogue.
5. **Bundle**: one lens, fitted from every panel's stars at once (`skypano.BundleLens`).
6. **Assemble**: `PlanCanvas` → `MatchPhotometry` → `Render` → `Flatten` → `Grade`.

## You do not sort the folder

Point the run at the whole night. Panels are segmented from the camera's **own gravity vector and
heading**, so `p01/`-style folders are unnecessary — and calibration frames mixed in are separated
first by `inspect.ClassifyRawStills`, because a dark shot without moving the tripod sits at exactly
the same pointing as the panel before it and no amount of geometry can tell them apart. Only the
pixels can.

Frames with no usable pointing metadata are **counted and reported**, never silently folded into
whichever panel came last.

```
curl -sS -XPOST localhost:8080/api/jobs -H 'content-type: application/json' \
  -d '{"path":"input/Iphone_10_08_2026","mode":"nightpano","format":"image"}'
```

A run that finds only one pointing says so and processes as a plain `milkyway` nightscape.

## Two things that are load-bearing, and why

**Background extraction runs ONCE, on the canvas — never per panel.** A panel is 57° × 72° of sky
filled edge to edge with the Milky Way, so flattening it against its own background subtracts the
subject: the band *is* the panel's large-scale gradient. The preset therefore forces `BackgroundAI`
and `BackgroundDegree` off and does the work on the assembled canvas, where the whole structure is
visible at once and a real galactic band can be told from a light-pollution dome. There the model is
a **low-order polynomial** fitted **outside the band**, with "inside the band" computed from galactic
latitude rather than guessed from brightness — the canvas knows its own frame.

**The lens is fitted once, shared by every panel.** Solved separately, each panel matches its own
stars at ~2 px RMS and looks solved, yet two panels place the same star up to 13 px apart, and the
blend averages those disagreeing copies into a short dash on every star in the picture. The cause is
uncorrected radial distortion (Apple's "lens corrected" ProRAW is not rectilinear), and a per-panel
fit **hides** it: matching inside a few pixels drops exactly the outer-field stars that carry it. See
`internal/skypano/bundle.go`. Measured on a real session, the shared fit took panel disagreement from
~560″ to ~110″ — under one canvas pixel.

## Knobs

Every `milkyway` grade knob applies (`look`, `brightness`, `saturation_scale`, `highlight_ceiling`),
plus the canvas:

| key | range | what it does |
|---|---|---|
| `projection` | `stereographic` \| `galactic` \| `altaz` \| `both` \| `all` | which canvas to draw |
| `scale_deg_per_pix` | 0.005–0.2 | canvas scale; 0.03 matches the panels |
| `group_step_deg` | 0.1–20 | how far frames may move before they count as a new pointing |
| `band_mask_lat_deg` | 0–60 | galactic latitude inside which pixels are band, not background |
| `pano_background` | bool | remove the residual sky dome from the canvas |
| `pano_foreground` | bool | composite the landscape under the arch (`altaz` only) |
| `keep_meteors` | bool | blend the meteors the clip rejected back in, minus satellites and aircraft |

`stereographic` is conformal, so star fields keep their shape — the natural "look up at the whole
sky" rendering. `galactic` lays the Milky Way out as a level band, which is the classic panorama of
it. `altaz` draws the **arch as it stood over the ground**, azimuth across and altitude up. `both`
writes the first two; `all` writes all three.

### The arch is drawn for ONE instant

`altaz` is the only canvas that needs to know *when*. The panels were shot over two hours, so the sky
turned about 30° underneath them; each is solved in equatorial coordinates, where that rotation does
not appear because equatorial coordinates turn with the sky. Ask instead where everything stood
relative to the **ground** and the question has a different answer for every panel.

Naming one instant makes it well-posed again. The middle of the session is chosen — it minimises the
largest correction any single panel gets — and the run reports it (`arch drawn as the sky stood at
02:27:30 UTC from 47.2767 N`). Stars shot later are therefore drawn where they had been earlier,
which is the only self-consistent choice and is exactly what one very wide exposure would have
recorded.

It needs the frames' **position and time**. Without them there is no horizon to draw the sky over, and
the run says so and skips that canvas rather than inventing one.

### The landscape under the arch

`pano_foreground` composites the ground into the `altaz` canvas, and only that one — the other
projections have no horizon to stand it on. It comes from whichever panel was aimed **lowest**, which
is not a choice: it is the only one with any ground in it.

Placing it takes one rotation and nothing else. The ground does not move in azimuth and altitude, and
the arch is drawn in azimuth and altitude, so a foreground panel is wrong only by the sidereal angle
between when it was shot and the instant being drawn. Turn its camera about the celestial pole by that
angle (`skypano.RotateAboutPole`) and the ordinary sky machinery puts the landscape where it stood — no
second canvas, no separate projection path.

The panel's own sky/ground mask travels with it, so the two agree pixel for pixel about where the
ground is. Those pixels are then removed from the **coverage** handed to the flatten and the grade: a
dark landscape inside their sample would drag the sky's black point down and grade the whole panorama
out washed. The ground still receives the same tone curve — it just does not get a vote on what that
curve should be.

Off by default, and soft-fails throughout: a panorama that lost its foreground is still a panorama.

### Meteors

`keep_meteors` searches the registered frames for streaks and blends the confident meteors back into
the linear sky before it is graded, so they are stretched and coloured with everything else instead of
being pasted on. Satellites and aircraft are identified and left out. Off by default: with it off a
run is byte-identical to one built before any of this existed.

The sigma-clip that builds the clean sky is *right* about a meteor — it is bright, it is in one frame,
and it is nothing like what that pixel does in the other thirty — and wrong only to discard it.

Detection is a **grey-scale opening along a line**, not a threshold and not a Hough transform: opening
with a line-shaped element of length L deletes everything that does not contain an unbroken run of L
pixels in the tested direction, so a star is gone at every orientation while a streak survives at its
own. Brightness never enters into it, which is what lets it find a meteor *fainter than the stars it
sits among* — the case that defeats every threshold. See `internal/meteor/streak.go`.

Every candidate is written to `meteors.json` with the reason it was kept or dropped, so a decision can
be argued with without re-running.

## Requirements

The **deep star catalogue** is required: `just download-deepstars`. At a 72° field Siril's plate
solver cannot help, so panels are solved against ATHYG by our own quad solver. Without it the run
stops after stacking and says so.

## Outputs

Under `output/<object>/<runID>/`:

- `pano_<projection>.png` — the graded panorama
- `pano_<projection>_linear.fits` — the linear canvas, for your own grading
- `panels/<label>/` — each pointing's own nightscape run, unchanged
- `panels/<label>/meteors.json` — every streak found, with its class and the reason (`keep_meteors`)
- `panels/<label>/meteor_layer.fits` — the linear layer that was blended in (`keep_meteors`)

## See also

- `internal/skypano` — the projections, solver, bundle, blend, flatten and grade
- `docs/modes/milkyway.md` — the per-panel recipe
- `docs/modes/mosaic.md` — the gnomonic, deep-sky tiled mosaic
