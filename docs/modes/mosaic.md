# Mosaic mode (tiled panels)

Objects larger than one camera field (M31, NGC 7000, the Veil — the FC-100 DF + ASI 1600MM Pro
field is ≈ 1.37° × 1.04°) are shot as **N overlapping panels**, one pointing each, and assembled
into a single seamless image. Mode id: `mosaic`. Entry point: `pipeline.ProcessMosaic`
(`internal/pipeline/mosaicmode.go`); the pure assembler lives in `internal/mosaic`.

> Not to be confused with the `union_canvas` knob (legacy wire alias `mosaic`): that keeps the
> union of several nights of the SAME pointing. Mode `mosaic` forces it off — the assembler owns
> placement.

## The end-to-end flow

1. **Plan** (web UI → Mosaic page): pick an object, the planner computes the tile grid from your
   optics (server-side `internal/mosaicplan`, persisted in `mosaic_plans` — see the plan JSON in
   `GET /api/mosaic/plans/{id}`). Default **20 % overlap**, one camera position angle for the whole
   mosaic (EQ mount ⇒ no field rotation; set it once at the home position with the Capture tab's
   star-chart preview).
2. **Capture**: one folder per panel — `p01/`, `p02/`, … in serpentine order (the capture
   assistant names them). `panel_1/` and `tile_A/` spellings are accepted too.
3. **Process**: `POST /api/jobs {"path": …, "mode": "mosaic", "format": "image",
   "mosaic_plan_id": N}` (the plan reference is optional — without it panels are detected from the
   folder names, else clustered from the frames' OBJCTRA/OBJCTDEC pointing headers at 0.35× the
   estimated field).

## What the pipeline does

Per panel (sequentially — Siril is the bottleneck and memory stays bounded):

1. The panel's lights become their own inventory subset (`inspect.Subset`) — a panel spanning
   several nights still groups per night with per-night flats + photometric normalization, exactly
   like a multi-night deepsky run. Calibration masters build ONCE from the full capture and match
   per group (panels share them automatically).
2. Per filter: the standard deepsky calibrate → grade → register → stack
   (`stackOneChannel`/`processChannelGroups`, reused verbatim). Work dirs `work/run_*/panel_pNN/`,
   masters under `output/<object>/<run>/panels/pNN/`.
3. `alignChannels` co-registers + parity-normalizes the panel's channels, then the panel is
   plate-solved **once** on its aligned broadband reference (L first — narrowband rarely solves),
   with a hint ladder: plan tile center → the panel's own header centroid → the run-level seed.
   An unsolvable panel is dropped with a loud warning; the run continues.

Assembly (`internal/mosaic`, pure Go):

4. **Canvas**: north-up/east-left TAN grid at the plan center (else the solve centroid), pixel
   scale from the anchor panel (nearest the center). Every panel is resampled exactly once —
   pasting the anchor unresampled would give it a visibly sharper PSF than its neighbors.
5. **Photometric match**: pairwise overlap fits (`v_B ≈ gain·v_A + offset`, robust quantile band,
   contamination guard degrades nebula-filled overlaps to offset-only), then an anchor-fixed
   weighted least squares over the whole overlap graph — loop closure prevents chained drift.
   Gains are fitted once on the broadband reference and shared across channels; offsets are refit
   per channel (sky pedestals are filter-dependent).
6. **Blend**: `canvas = Σ w·(v·gain+offset) / Σ w` with per-panel feathered weights — smoothstep of
   the chamfer distance to the panel edge (`feather_frac × overlap × min(W,H)` px). **This is what
   keeps every star round**: near a panel's edge its own weight → 0 while the neighbor's interior
   weight ≈ 1, so the optically softest, most elongated stars of each panel are dominated in the
   blend by the neighbor's on-axis rendering of the same stars.
7. **Crop** (`canvas_crop`): `common` (default) = the intersection of every channel's covered box;
   `union` = keep everything; `plan` = the plan's grid bbox.
8. The per-channel canvases are written as `output/<object>/<run>/aligned_<tag>.fits` **with a real
   TAN WCS header** — then the UNCHANGED standard finish runs (`finishAligned`: GIMP LRGB+Ha
   composite, SPCC — solve-free thanks to the header — GraXpert, star annotation, mono outputs).
   Post-run **Refine** (Tier A/B) works out of the box because refine reconstructs channels from
   those same `aligned_*` files. Tier-C re-stack and the editable-stage rerun are not wired for
   mosaic in v1.

## Knobs (`params`, mode `mosaic` — plus the whole deepsky finish surface)

| Key | Default | Meaning |
|-----|---------|---------|
| `overlap_expected` | 0.20 | capture overlap the plan promised (feather + segmentation scale) |
| `feather_frac` | 0.6 | blend ramp width as a fraction of the overlap |
| `photom_match` | `gain_offset` | `gain_offset` \| `offset` \| `off` |
| `canvas_crop` | `common` | `common` \| `union` \| `plan` |
| `min_panel_frames` | 3 | drop panels with fewer stacked frames |
| `panel_source` | `auto` | `auto` \| `folders` \| `coords` |

## Failure behaviour

- No panel structure found → the run degrades to a plain deepsky run with a warning.
- A panel that fails to stack, stacks under `min_panel_frames`, or cannot be plate-solved is
  **dropped loudly** (`run.json → mosaic_assembly.panels[].drop_reason`); the mosaic assembles from
  what remains. Placement is the one thing that cannot soft-degrade silently.
- Photometric matching failure assembles uncorrected (warning); a channel that cannot be assembled
  is skipped (warning); no channel assembled → the run fails honestly.

## v1 limits

Mono + filter-wheel panels only (OSC panels: stack individually with the OSC path for now);
cross-session reuse and the low-disk S3 stager are off; no Tier-C supervised re-stack; no relative
placement for unsolvable panels (they are dropped rather than star-matched to a neighbor).

`run.json → mosaic_assembly` records the grid, per-panel solves + gains/offsets, pairwise overlap
fits and per-channel seam RMS — a bad seam is diagnosable, never silent.
