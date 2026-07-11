# Embedded deep-sky catalogue snapshot

These CSVs are a verbatim copy of the deep-sky object catalogues bundled with **Siril** (macOS 1.4.x
app bundle, `Contents/Resources/share/siril/catalogue/`). They are compiled into the `astrostack`
binary via `go:embed` (see `../embed.go`) and used as the **fallback** catalogue for the tonight
planner and the name→coordinate resolver whenever no readable on-disk Siril catalogue is found —
e.g. inside the Docker engine image, where the Linux distro Siril ships a different (legacy
semicolon-delimited `.txt`) catalogue format the CSV parser can't read, or on a host with no Siril
installed.

When Siril *is* installed and its CSV catalogue is readable (the macOS host-engine path, and the
amd64 AppImage path), that on-disk catalogue is preferred and these embedded copies are never touched.

Format (header-driven, parsed by `records.go`): `name,ra,dec[,diameter,mag,alias]` — RA/Dec in decimal
degrees, aliases `/`-separated. Files: `messier`, `ngc`, `ic`, `sh2`, `ldn`.

**Provenance / licence.** The coordinates, sizes and magnitudes are public-domain astronomical data
(the Messier, NGC/IC, Sharpless and Lynds Dark Nebula catalogues). Siril is GPL-licensed; these data
files carry no additional restriction. To refresh, re-copy them from a current Siril install.

## `openngc.csv` — object-type overlay (OpenNGC)

`openngc.csv` is a **slimmed snapshot of OpenNGC** (https://github.com/mattiaverga/OpenNGC), the
morphological type / size / surface-brightness / common-name database for the NGC and IC catalogues.
The Siril CSVs above carry no *type* column, so every object without a type keyword in its name (e.g.
NGC 6946) fell to "other" in the tonight table; this overlay supplies the authoritative type and
enriches size, surface brightness, Hubble morphology and common names.

It is **not** a coordinate source — it is matched by name onto the Siril records above (which own the
coordinates) in `openngc.go`, so `Dup`/`NonEx`/star rows are dropped and only NGC/IC names are kept.
Columns (header-driven, parsed by `openngc.go`): `name,type,majax,minax,posang,surfbr,hubble,messier,common`
— name unpadded (`NGC0224`→`NGC224`) to match the Siril rows, `common` names `/`-separated.

**Licence.** OpenNGC data is released under **CC-BY-SA-4.0** (© Mattia Verga and contributors). Keep
this attribution when redistributing. To refresh, re-slim `database_files/NGC.csv` from a current
OpenNGC release (see the awk recipe in the commit that added this file).
