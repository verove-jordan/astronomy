# skymap.json — sky-map dataset (stars + constellation figures)

`skymap.json` is the offline dataset the GoTo "Find it in the sky" map renders (stars, constellation
lines, constellation-name labels). It is **generated**, not hand-edited — regenerate with:

```
just gen-skymap-data          # default: stars to magnitude 6.0 (naked-eye limit)
just gen-skymap-data 5.5       # lighter (fewer background stars)
```

(equivalently `go run ./cmd/astrostack skymap-data --mag 6.0`). The generator lives in
`internal/skymapgen/`; it fetches the sources over the network **at build time only** — the shipped app
never touches the network for this.

## Schema (compact)

```jsonc
{
  "magLimit": 6.0,
  "source": "…",
  "stars": [[raDeg, decDeg, mag], …],   // J2000; ra/dec rounded to 1e-3°, mag to 1e-2
  "names": [[starIndex, "Vega"], …],     // proper-named stars only (for labels)
  "lines": [[i, j], …],                  // constellation figure segments, indices into `stars`
  "constellations": [{ "name": "Lyra", "ra": …, "dec": … }, …]  // label centroids
}
```

Positions are J2000 equatorial; the frontend converts to alt/az for the observer's site and time
(`frontend/src/utils/astro.ts`, mirroring `internal/astro`). Stars referenced by a constellation line are
always included even if fainter than `magLimit`, so figures never break.

## Sources & licences

- **Stars** — HYG database v4.1 (`astronexus/HYG-Database`, `hyg/CURRENT/hygdata_v41.csv`). Licence:
  Creative Commons Attribution-ShareAlike. RA is stored in hours (converted ×15 to degrees).
- **Constellation figures + names** — Stellarium _western_ sky culture
  (`skycultures/western/constellationship.fab` + `constellation_names.eng.fab`), pinned to tag `v0.21.3`
  which still ships the classic `.fab` format. Licence: GPL-2.0-or-later.

Both licences are compatible with this project. Keep this attribution when redistributing the dataset.
