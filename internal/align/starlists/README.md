# Hand-controller alignment star lists

These CSVs restrict the GoTo alignment suggestions to stars the user's hand controller can
actually offer, labeled exactly as the controller displays them. Schema:

```csv
hc_name,catalog_name
```

- `hc_name` — the label as the hand controller shows it (what the UI displays prominently).
- `catalog_name` — the matching `name` in `../brightstars.csv`; **empty means identical to
  `hc_name`** (the common case), so alias rows stand out.

Rows whose `catalog_name` does not resolve in `brightstars.csv` are skipped at load;
`starlist_test.go` asserts there are none, so drift is caught in CI.

## celestron.csv (82 stars)

Transcribed from the Celestron knowledge-base article "Can all named stars listed in the hand
control be used for alignment?" — the NexStar/NexStar+ hand controls (AVX, SE, Evolution…)
offer their named stars brighter than magnitude ~2.5 as alignment/calibration choices.
<https://www.celestron.com/blogs/knowledgebase/can-all-named-stars-listed-in-the-hand-control-be-used-for-alignment>

Aliases (HC spelling → our catalog name): `Alpha Centauri`→Rigil Kentaurus, `El Nath`→Elnath,
`Regor`→Gamma Velorum, `Phad`→Phecda, `Tsih`→Gamma Cassiopeiae, `Scutulum`→Aspidiske.

Applies to the `celestron-eq` profile only. **Celestron SkyAlign (`celestron-altaz`) is
intentionally unfiltered** — SkyAlign accepts any three bright objects; the HC identifies them
itself, so no named-star restriction applies.

## synscan.csv (~148 stars)

The SkyWatcher SynScan hand controller's alignment-star list is not published by the vendor as
a single document; this file is the union of two published reconstructions, filtered to
magnitude ≤ 3.5 (the profiles' `MagLimit` — fainter rows could never be selected anyway) and to
names resolvable in `brightstars.csv`:

- waloszek.de SynScan named/alignment star tables (northern sky):
  <http://www.waloszek.de/astro_sw_star_disc_as_e.php>
- Brisbane Astronomical Society, "SkyWatcher SynScan alignment stars — southern hemisphere":
  <https://bas.asn.au/wp-content/uploads/SkyWatcher-SynScan-alignment-stars-southern-hemisphere.pdf>

SynScan-specific aliases: `Graffias`→Acrab, `Zubeneshamali`→Zubeneschamali, `Lesuth`→Lesath,
`Celbalrai`→Cebalrai, `Nair Saif`→Hatysa, `Deneb Algiedi`→Deneb Algedi, `Sulaphat`→Sulafat,
`Minkar`→Epsilon Corvi, plus `Regor`/`Scutulum` as on the Celestron list.

**Caveats.** The SynScan list varies slightly across hand-controller firmware versions
(V3/V4/V5) and third-party sources disagree on a few spellings; a suggestion the controller
lacks is recoverable in the UI (Skip → instant replacement). Intentionally omitted because they
are absent from `brightstars.csv` (add them there first if ever wanted): Alniyat, Propus,
Kekwan, Tchou, Skat, Nasl, Homam, Kaffaljidhm, Tarf, Mothallah, Asmidiske (ξ Pup). Mira is
excluded deliberately (variable, mag 2–10).

## Refreshing a list

Edit the CSV (keep the header), keeping `hc_name` as the exact controller label and
`catalog_name` only when it differs. Run `go test ./internal/align/` — the integrity test fails
on any row that no longer resolves, any duplicate, or any member fainter than mag 3.5.

## Licence

Star names and positions are public-domain astronomical data; the lists are transcribed from
the vendors' published documentation for interoperability.
