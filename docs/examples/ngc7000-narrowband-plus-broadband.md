# NGC 7000 — narrowband nebulosity + broadband stars, from one mixed folder

A repeatable procedure for the case where **one capture folder holds both dual-band and broadband
nights of the same target**, and you want the nebulosity from the narrowband data and the stars,
star colour and sky from the broadband data.

Companion to [ngc7000-coloured-detailed.md](ngc7000-coloured-detailed.md), which explains the
single-session duo-band path in depth. Read that one first for *why* the colour comes out the way it
does; this one is the *how* for the two-filter composite.

Rig throughout: RedCat 51 (250 mm f/4.9) + ASI2600MC Air (IMX571, RGGB, 6248×4176, 3.76 µm),
3.116 ″/px, field 5.4° × 3.6°.

---

## 0. Identify which nights are which — from the pixels, not the dates

The FITS carry **no `FILTER` card** and the filenames carry no filter token, so nothing but the
capture night separates dual-band from broadband. Verify before trusting your memory of the session:

| signature | dual-band | broadband |
|---|---|---|
| sky above the bias pedestal, per 120 s | **2–6 ADU** | **30–45 ADU** |
| background colour | red-dominant (R > G > B) | green/blue-dominant |
| star density, same exposure | baseline | **~4× higher** |
| stellar colour | sparse, muted | obvious tints |

`POST /api/inspect` gives the per-night breakdown (`sessions[].configs[]`). If a night is ambiguous,
average ~30 of its subs without registration — the nebula shape and its colour will be unmistakable.

## 1. Split the nights into separate folders

Do **not** try to do this with `exclude_sets`. It works, but the exclusion only filters the current
scan: any frame catalogued by an earlier run over the same folder is re-added by the cross-session
reuse pass, silently. (Fixed — see `currentScanPaths` in `internal/pipeline/reuse.go` — but a folder
split is still the clearer, verifiable option.)

Hard links cost no disk and leave the originals untouched:

```bash
cd input
for d in NGC7000-NB NGC7000-BB; do mkdir -p "$d"/{Light,Dark,Flat,Bias}; done
# calibration frames go to BOTH (the matcher picks per exposure/temp/session)
for t in Dark Flat Bias; do
  for f in NGC7000-unified/$t/*.fit; do
    ln "$f" "NGC7000-NB/$t/$(basename "$f")"; ln "$f" "NGC7000-BB/$t/$(basename "$f")"
  done
done
# lights by capture night
for f in NGC7000-unified/Light/*.fit; do b=$(basename "$f"); case "$b" in
  *_20260831-*|*_20260903-*|*_20260904-*) ln "$f" "NGC7000-NB/Light/$b" ;;   # dual-band
  *_20260906-*)                            ln "$f" "NGC7000-BB/Light/$b" ;;   # broadband
esac; done
```

**Always pass `"reuse_disabled": true`** on runs over these folders. The same physical dark now
exists at three paths, and cross-session raw-calib pooling counts frames per *path*: without it the
deep-master builder stacks each dark three times (bias 160, darks 135/138 instead of 90/45/46) and
writes those masters into `library/`.

## 2. Stack the narrowband night(s)

```bash
curl -sS -XPOST localhost:8080/api/jobs -H 'content-type: application/json' -d '{
  "path":"input/NGC7000-NB","mode":"nebula","format":"image","target":"NGC 7000",
  "focal_mm":250,"pixel_um":3.76,
  "color_calibration":false, "denoise":true, "ha_exclude_stars":true,
  "register_2pass":true, "palette":"hoo", "reuse_disabled":true,
  "params":{"trail_mask_k":0,"crop_frac":0.05,"background_level":0.10,"saturation":0.20,
            "star_desat":0.30,"denoise_chroma":0.85,"chroma_smooth_px":3,"chroma_bg_smooth_px":8}}'
```

- `palette: "hoo"` is what makes this work: it triggers the duo-band splitter
  (`internal/pipeline/duoband.go`), which writes **`syn_Ha.fits`** (red pixels) and
  **`syn_OIII.fits`** (per-pixel max of green and blue) into the run dir. A narrowband palette also
  makes the finish skip SPCC **and SCNR**, so the [OIII] green is not stripped.
- `color_calibration: false` — SPCC is meaningless on emission-line data.
- `trail_mask_k: 0` — on data this faint the mask costs 26 % of every frame and finds nothing.
- `focal_mm`/`pixel_um` are **not optional**: the engine defaults to 740 mm / 3.8 µm, and the guard
  that rejects foreign optics compares pixel size with a 5 % tolerance, so 3.76 vs 3.8 passes
  silently and the solver is handed 740 mm for a 250 mm scope.

## 3. Stack the broadband night — for STARS, not depth

```bash
curl -sS -XPOST localhost:8080/api/jobs -H 'content-type: application/json' -d '{
  "path":"input/NGC7000-BB","mode":"deepsky","format":"image","target":"NGC 7000",
  "focal_mm":250,"pixel_um":3.76,
  "color_calibration":true, "denoise":true, "register_2pass":true,
  "palette":"natural", "reuse_disabled":true,
  "params":{"crop_frac":0.03,"background_level":0.08,"star_reduce":0,
            "roundness_floor":0.66,
            "color_denoise_ai":false,"chroma_smooth_px":0,"chroma_bg_smooth_px":0,
            "sky_chroma_flatten_px":0,"denoise_chroma":0.35,"saturation":0.50,"star_desat":0}}'
```

Two things here are the difference between a grey star field and a colourful one:

- **The four chroma-reducing stages are off.** `deepsky` normally runs AI colour denoise +
  `chroma_smooth_px 6` + `chroma_bg_smooth_px 24` + sky-chroma flattening. That is right for a deep
  broadband stack and far too much for a thin one: measured, it **halved** the chroma (0.138 → 0.073)
  before GIMP ever saw it. Off, the GIMP base goes from median 0.159 → 0.192 and p90 0.235 → 0.396.
  It also runs in ~3.5 min instead of ~25 (the AI denoise was 19 of those).
- **`roundness_floor: 0.66`** keeps only well-guided subs. The default 0.55 is ~1.8:1 elongation, and
  the sigma tests are relative to the *session median* — so a night that is mostly trailed defines
  its own "normal" and nothing gets cut. Check the per-frame `roundness` in `run.json` and set the
  floor just under the good population.

**Run the two jobs one at a time.** (Concurrent runs on the same target used to share
`<work>/run_<id>`; fixed by `reserveRunDir`, but sequential is still kinder to RAM — Siril peaks
around 6 GiB per stack.)

## 4. Align the narrowband master onto the broadband grid

Register the narrowband **`master_RGB.fits`** (the true colour master — most stars) against the
broadband `rgb_base.fits`. Both are 6248×4176, so no padding is needed; Siril's triangle matching is
rotation-invariant and absorbs a meridian flip.

```bash
W=work/composite; mkdir -p $W
cp <BB_RUN>/rgb_base.fits   $W/a_bb.fits    # frame 1 = reference = the grid we keep
cp <NB_RUN>/master_RGB.fits $W/b_nb.fits
cat > $W/reg.ssf <<'SSF'
requires 1.2.0
setext fits
set32bits
cd <ABS PATH TO $W>
link comp -out=.
register comp -transf=homography -interp=lanczos4
SSF
siril-cli -s $W/reg.ssf
```

Sanity check the log: expect ~1800 matched pairs and **Inliers: 1.000**. Then split
`r_comp_00002.fits` into Hα (red plane) and [OIII] (per-pixel max of green and blue) — the same
arithmetic `duoband.go` uses, now on the broadband pixel grid.

## 5. Composite: broadband base + emission screens

This mirrors `internal/gimp/compose.go`'s recipe — what AstroStack already does for a mono LRGB+Ha
rig, applied across two runs. Broadband RGB is the **base** (every star, every star colour, the sky);
Hα is screened tinted **pure red**, [OIII] tinted **teal**; both get point sources removed and a
black point clipped, so the emission lands on nebulosity and never tints the stars.

Three settings decide how it looks:

- **Black point ≥ the layer's background.** Below it the red screen lifts the *whole sky* and the
  frame goes uniformly pink. With the layers stretched to a 0.12 background, clip at ~0.14.
- **Per-layer stretch vs shared stretch** — this is the entire red-vs-teal question:
  - *Per-layer (unlinked)*: each line normalised to its own background and its own noise. [OIII]'s
    noise is **3× lower** than Hα's (green has two Bayer pixels per tile), so this boosts it ~3×.
    The median line ratio is 0.382 and 1/0.382 = 2.6, so [OIII] **overtakes** Hα across the brighter
    nebula → teal core, red rims. This is the conventional HOO look and what the previews show.
  - *Shared*: preserves the true Hα:[OIII] amplitude. NGC 7000 is genuinely Hα-dominant, so this is
    honest and comes out mostly red.
- **Star removal on the emission layers** (median blur, or a morphological opening) — without it the
  teal screen tints bright stars cyan.

Verify the [OIII] is real before boosting it: on this data 86.9 % of Hα-bright pixels are also 5σ in
[OIII], median ratio 0.382 (p90 0.72), with coherent structure. It is signal, not noise — but note it
is `max(G,B)` from a dual-band filter, not a real [OIII] exposure, so treat its *amplitude* as a
rendering choice rather than photometry.

---

## Capture-side notes for the next session

In order of how much they would improve the result:

1. **Guiding.** Star elongation here is *inside* each exposure — a single unregistered sub shows the
   same axis ratio as the 7-frame stack (0.705 vs 0.698), so no amount of frame selection or
   stacking fixes it. Polar alignment is fine (measured field rotation 0.023° over 30 min → ~0.2 px
   of smear in a 300 s sub; drift 3.6 px over 30 min). Look at guide aggressiveness, RA periodic
   error and Dec backlash instead.
2. **Gain 100 for narrowband.** The ASI2600MC switches to HCG at 100 and read noise drops ~3.5 e⁻ →
   ~1.5 e⁻. With the dual-band sky only 2–6 ADU above the pedestal you are read-noise limited, which
   is the one regime where gain is nearly free signal. Broadband can stay at gain 0.
3. **Flats.** Aim for 30–50 % of full scale; 2.5 %/4.4 % gets flagged by QC as too dim.
4. **Shoot flats per filter and per night** — a broadband night should not borrow a dual-band night's
   flats.
5. **StarNet++** is not bundled (licence). Install it and set `STARNET_BIN` to unlock star reduction,
   which is the biggest single improvement available for a star field this elongated.
