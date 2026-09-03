# Processing modes

`internal/mode/preset.go` maps each capture mode to a `Preset` that retunes the whole pipeline
(grading, background extraction, stretch, Ha blend, saturation, curves, AI toggles). Each mode has
its own entry point and its own finish; all of them share the run contract (a per-run output
directory `output/<object>/<runID>/` with a durable `run.json`), the soft-fail philosophy (a missing
optional tool degrades the result, never fails the run), and the opt-in AI finish supervisor.

| Mode | What it does | Entry point | Doc |
|------|--------------|-------------|-----|
| `deepsky` | Mono LRGB(+Ha) **or one-shot colour** galaxies/clusters: per-channel calibrate → grade → stack, co-register, GIMP LRGB+Ha finish with SPCC colour | `pipeline.Process` (`internal/pipeline/pipeline.go`) | [deepsky.md](deepsky.md) |
| `nebula` | Same engine as deepsky, retuned for large faint emission objects: lenient grading, Ha-forward blend, StarNet++ star reduction | `pipeline.Process` (`internal/pipeline/pipeline.go`, nebula preset) | [nebula.md](nebula.md) |
| `milkyway` | One-shot-colour nightscape (iPhone DNG/HEIC, DSLR raws): photometric develop, sky-only stack, foreground composite, data-driven grade | `pipeline.ProcessOSC` → `processNightscape` (`internal/pipeline/osc.go`) | [milkyway.md](milkyway.md) |
| `nightpano` | Sky panorama: a hand-swept arc of pointings, each stacked by the milkyway recipe, then plate-solved, fitted to ONE shared lens and reprojected onto a spherical canvas | `pipeline.ProcessNightpano` (`internal/pipeline/nightpano.go`) | [nightpano.md](nightpano.md) |
| `planetary` | Lucky imaging (Moon/planets): native-res sharpness ranking, multi-point warp, AP-weighted stack, RL deconvolution | `pipeline.ProcessPlanetary` (`internal/pipeline/planetary.go`) → `planetary.Process` (`internal/planetary/planetary.go`) | [planetary.md](planetary.md) |
| `comet` | Moving comet (mono or colour): one global star alignment, auto-fit motion track, dual star/comet stacks, StarNet star-layer recomposite | `pipeline.ProcessComet` (`internal/pipeline/comet.go`) | [comet.md](comet.md) |
| `livestack` | Watch a folder/S3 prefix during capture: calibrate each new sub once, incrementally re-stack with a live preview, finalize with the full pipeline on Stop | `livestack.Run` (`internal/livestack`) → `pipeline.Process` | [livestack.md](livestack.md) |
| `sun` | The Sun in Hα or white light: triage a mixed folder by measured disc size, limb-register, window, stack and finish in Go | `pipeline.ProcessSun` (`internal/pipeline/sunmode.go`) + `internal/solar` | [sun.md](sun.md) |
| `eclipse` | A partially eclipsed Sun: the solar recipe measured against TWO circles, with the occulting Moon masked out of the stack and of every on-disc measurement; `sequence_panels` also renders the whole eclipse as one progression sheet | `pipeline.ProcessSun` via `mode.For(Eclipse)` + `internal/solar/pair.go`, `internal/eclipsegeom` | [eclipse.md](eclipse.md) |
| `mosaic` | Tiled panels of one large object (mono or colour): per-panel deepsky stacks, one plate-solve per panel, WCS assembly onto a single photometrically-matched canvas, standard finish | `pipeline.ProcessMosaic` (`internal/pipeline/mosaicmode.go`) + `internal/mosaic` | [mosaic.md](mosaic.md) |

`livestack` is not a separate *recipe*: the live session incrementally re-stacks while capturing
and **finalizes through the deepsky path** — its preset is the deepsky preset retagged
(`mode.For` in `internal/mode/preset.go`, `internal/pipeline/live.go`).

## Monochrome or colour — the same modes

Every mode above except `milkyway` accepts **either** monochrome frames from a filter wheel **or**
one-shot colour: a DSLR/mirrorless raw (NEF/CR2/CR3/ARW/RAF/DNG), a colour camera's Bayer CFA FITS,
or already-demosaiced RGB TIFF/PNG/JPEG. Which one you have is decided while inspecting the folder
(`inspect.Inventory.ColorModel`), not by the mode you pick, so there is nothing to configure and no
separate colour mode to remember.

A colour run is the same pipeline with **one channel, named `RGB`** — which is why it inherits the
calibration library, grading, trail masking, plate-solving, SPCC, denoise and the whole finish
rather than reimplementing them. Three things differ, and only three:

1. **Ingest.** A raw CFA mosaic reaches Siril *undebayered*, so the master flat and the defect map
   are applied to the sensor's own pixels and demosaicing happens last. Debayer first and every hot
   pixel and dust shadow has already been smeared across its neighbours.
2. **No combine.** The stacked master is already RGB, so the LRGB `rgbcomp` step is skipped.
3. **No palette.** The palettes map *filters* onto output channels, which a colour sensor does not
   have; the master passes through in colour with no emission screens. Narrowband or dual-band
   filters on a colour sensor are out of scope.

A folder holding **both** monochrome and colour lights is reported as `mixed`: no single run can
stack both, so the monochrome session is processed and the colour frames are named in a warning
rather than silently dropped.

`milkyway` is the exception, and deliberately so: its nightscape recipe (develop → sky-only stack →
foreground composite) is a different procedure, not a filter variant.

Cross-mode reference material:

- [../pipeline.md](../pipeline.md) — the shared pipeline stages, AI enhancement, cross-session reuse.
- [../architecture.md](../architecture.md) — components, containerized mode, S3 storage.
- [../verification.md](../verification.md) — per-mode end-to-end verification recipes with pass criteria.
