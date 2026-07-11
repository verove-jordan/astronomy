# Processing modes

`internal/mode/preset.go` maps each capture mode to a `Preset` that retunes the whole pipeline
(grading, background extraction, stretch, Ha blend, saturation, curves, AI toggles). Each mode has
its own entry point and its own finish; all of them share the run contract (a per-run output
directory `output/<object>/<runID>/` with a durable `run.json`), the soft-fail philosophy (a missing
optional tool degrades the result, never fails the run), and the opt-in AI finish supervisor.

| Mode | What it does | Entry point | Doc |
|------|--------------|-------------|-----|
| `deepsky` | Mono LRGB(+Ha) galaxies/clusters: per-channel calibrate → grade → stack, co-register, GIMP LRGB+Ha finish with SPCC colour | `pipeline.Process` (`internal/pipeline/pipeline.go`) | [deepsky.md](deepsky.md) |
| `nebula` | Same engine as deepsky, retuned for large faint emission objects: lenient grading, Ha-forward blend, StarNet++ star reduction | `pipeline.Process` (`internal/pipeline/pipeline.go`, nebula preset) | [nebula.md](nebula.md) |
| `milkyway` | One-shot-colour nightscape (iPhone DNG/HEIC, DSLR raws): photometric develop, sky-only stack, foreground composite, data-driven grade | `pipeline.ProcessOSC` → `processNightscape` (`internal/pipeline/osc.go`) | [milkyway.md](milkyway.md) |
| `planetary` | Lucky imaging (Moon/planets): native-res sharpness ranking, multi-point warp, AP-weighted stack, RL deconvolution | `pipeline.ProcessPlanetary` (`internal/pipeline/planetary.go`) → `planetary.Process` (`internal/planetary/planetary.go`) | [planetary.md](planetary.md) |
| `comet` | Moving comet: one global star alignment, auto-fit motion track, dual star/comet stacks, StarNet star-layer recomposite | `pipeline.ProcessComet` (`internal/pipeline/comet.go`) | [comet.md](comet.md) |
| `livestack` | Watch a folder/S3 prefix during capture: calibrate each new sub once, incrementally re-stack with a live preview, finalize with the full pipeline on Stop | `livestack.Run` (`internal/livestack`) → `pipeline.Process`/`ProcessOSC` | [livestack.md](livestack.md) |

`livestack` is not a separate *recipe*: the live session incrementally re-stacks while capturing
and **finalizes through the deepsky (or OSC) path** — its preset is the deepsky preset retagged
(`mode.For` in `internal/mode/preset.go`, `internal/pipeline/live.go`).

Cross-mode reference material:

- [../pipeline.md](../pipeline.md) — the shared pipeline stages, AI enhancement, cross-session reuse.
- [../architecture.md](../architecture.md) — components, containerized mode, S3 storage.
- [../verification.md](../verification.md) — per-mode end-to-end verification recipes with pass criteria.
