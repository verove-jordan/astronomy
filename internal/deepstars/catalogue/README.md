# Embedded deep star catalogue

`hyg_mag9.csv.gz` — 83,479 stars at magnitude ≤ 9.0, gzipped CSV, `go:embed`-ed by
`internal/deepstars`. It powers the star-name annotation (`stars.json` labels): J2000 positions,
V magnitude, proper motion, and the designation chain (proper name → Bayer → Flamsteed → HD).

## Source & regeneration

- Source: **HYG Database v4.1** (David Nash, astronexus.com) — the exact same pinned URL
  `gen-skymap-data` uses for `frontend/src/assets/skymap.json`
  (`skymapgen.DefaultHYGURL`, raw.githubusercontent.com/astronexus/HYG-Database).
- Regenerate with `just gen-deepstars-data` (optionally `just gen-deepstars-data 8.5` for a
  different depth). Network is used at generation time ONLY; commit the regenerated file.
- The generator (`internal/deepstars/gen`) hard-fails on any Bayer token it does not recognise,
  so the runtime greek-letter map can never silently miss a designation.

## Columns

`ra_deg,dec_deg,mag,proper,bayer,flam,con,hd,pmra,pmdec`

- `ra_deg`/`dec_deg`: J2000, degrees, 4 decimals (≈0.36″ — far below the label match tolerance)
- `mag`: V magnitude, 2 decimals; rows are magnitude-sorted brightest-first
- `proper`: IAU-style proper name ("Vega") or empty
- `bayer`: HYG token ("Alp", "The-2") or empty; `flam`: Flamsteed number or empty
- `con`: 3-letter IAU constellation; `hd`: Henry Draper number or empty
- `pmra`/`pmdec`: proper motion, integer mas/yr (pmra includes the cos δ factor, Hipparcos-style)

## Licence

HYG Database is **CC BY-SA 4.0** — attribution: “HYG Database v4.1, David Nash /
astronexus.com”. This slimmed extract keeps the same licence.
