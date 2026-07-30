# S3 storage

Captures, results, the calibration library and full backups can mirror to any S3-compatible
storage (AWS, MinIO, Scaleway, …) so local disk stays small without losing data. Everything is
soft-fail and verified: **no local file is ever deleted without confirming its copy on S3 first.**
Code: `internal/api/s3.go` + `s3conn.go`, `internal/transfer`, `internal/job/stager.go` +
`libpuller.go`, `internal/libmirror`, `internal/backup`, `internal/secret`.

## Connections & secrets

Two ways to give the engine credentials:

- **UI connections** (Processing → Storage): endpoint / access key / secret saved in Postgres,
  with the secret **AES-256-GCM encrypted at rest** (`internal/secret`). The master key comes from
  `ASTRO_ENCRYPTION_KEY` (base64, 32 bytes) or an auto-generated key file kept **outside** the
  backup roots — so a database dump alone can never leak usable credentials. The secret is
  decrypted only to build a client and is **never returned to the UI or logged**. One connection
  is the *default*; it drives the pipeline and all jobs.
- **Env fallback** (`ASTRO_S3_*` — see [configuration.md](configuration.md)) for headless setups.

Bucket and key prefix are per-request UI state, so several buckets/prefixes can coexist. A plain
**bucket file manager** (create/delete buckets, browse/move/upload/download objects) lives on the
same page.

## Mirror layout

Everything lives under the user-chosen prefix:

| Key | Contents |
|---|---|
| `<prefix>/data/<relative-path>` | capture-data mirror (a classified ledger maps local files to keys) |
| `<prefix>/output/<relative-path>` | finished stacks + run reports |
| `<prefix>/library/<file>` | calibration masters — flat `master_*` / `phone_master_*` FITS plus their `.sig` reuse signatures and `_defects.lst` bad-pixel maps. The multi-GB `catalogues/` subtree is **never** mirrored |
| `<prefix>/backup/<stamp>/` | full snapshots (see below) |

## Transfers (Storage tab / `POST /api/s3/transfer`)

Four operations, each running as a job in a dedicated transfer lane (they never starve processing
jobs), parallel per file (default 6, `ASTRO_S3_CONCURRENCY`), resumable via pause:

- **Upload / Sync** — copy folders up; sync skips objects that already match.
- **Download** — restore folders locally.
- **Free local** (`removeLocal`) — the space-saver: every file is **verified on S3
  (content check) before its local copy is deleted**; a folder with any unverifiable file is
  left untouched.

Freed data stays fully usable: the browser merges S3-only folders into the Import browser, and
previews/results serve **local-first with an S3 fallback** — a freed file is fetched into a
regenerable serve cache (`work/cache/s3/…`) on demand.

## Full-S3 processing

A job launched with storage mode **"Full S3 (free local after)"** pulls its inputs from the
mirror, runs locally (the engine itself never streams pixels from S3), pushes inputs + outputs
back, then frees the local copies (verified). With `ASTRO_S3_LOW_DISK=true` (default) the pull is
**staged in waves** — calibration frames first, then one channel's lights at a time, freed right
after that channel stacks — so peak local disk stays around one channel's worth of frames.
Transient S3 errors auto-pause the job (it resumes without recomputing; already-pushed files are
skipped).

## Library mirror

“Copy library to S3” (Library tab / `POST /api/library/s3-sync`) mirrors the calibration masters.
From then on the library behaves like a synced cache: when a run **matches** a master that is
absent locally, the engine pulls just that file (and its `_defects.lst` sidecar) on demand right
before Siril reads it, and frees transiently-pulled copies after the run. A machine that has never
built a dark can still calibrate with the pool's deep master.

## Backup & restore

`POST /api/backup` snapshots to `<prefix>/backup/<stamp>/`, componentwise (each soft-fails
independently; a manifest written last marks completeness):

| Component | Contents |
|---|---|
| `db.dump` | full Postgres dump (`pg_dump -Fc`) — catalog, jobs, presets, connections |
| `library.tar` | the calibration masters (catalogues excluded) |
| `atlas/` | the offline light-pollution atlas |
| `appstate.json` | **browser-only** state — favorites, setups, preferences, AstroAgent chats — assembled by the UI (`GET /api/backup/appstate` + client IndexedDB) and re-applied browser-side on restore |

**Restore is destructive** for the database (`pg_restore --clean --if-exists`) — it replaces the
current catalog/jobs with the snapshot. `.env` secrets are never included in backups.

## Glacier (cold storage classes)

Any S3 object can live on a **cold storage class** to cut cost. Two families
(`internal/s3store/glacier.go`):

| Family | Classes | Read now? | Notes |
|---|---|---|---|
| **Instant** | `STANDARD` (`""`), `STANDARD_IA`, `ONEZONE_IA`, `INTELLIGENT_TIERING`, **`GLACIER_IR`** | yes | `GLACIER_IR` is cheap archival with *instant* reads — no thaw |
| **Archived** | **`GLACIER`** (Flexible), **`DEEP_ARCHIVE`** | no — must **restore** first | thaw takes minutes → ~48 h |

Rules the code encodes: `""` means `STANDARD`; a `HEAD`/`Stat` works on an archived object (only
`GET`/`CopyObject` fail with `InvalidObjectState`); a **class change is a `CopyObject` onto the same
key** with `ReplaceMetadata` that carries the `Astro-Md5` + content-type forward (so remove-local's
strong verification and served MIME types keep working; the `s3_objects` ledger is untouched — the key
and size don't change); objects > 5 GiB transition via `ComposeObject`. Everything **soft-fails** on an
endpoint that has no restore API (`ErrRestoreUnsupported` on 501/405 — e.g. MinIO): the class controls
simply do nothing and a run is never blocked.

### The thaw is a durable, visitable Task

A restore is asynchronous and long, so a job waiting on one is **not** a held worker. It parks as a
`causeThaw` pause (`internal/job/pause.go`) — zero workers, survives an engine restart — and the
existing 60 s auto-resume sweep re-checks it on a **2 → 15 min** backoff, bounded by a **48 h**
deadline, then finishes automatically. You can open the task any time to see "Thawing from Glacier —
retrieval window ~Nh left". Retrieval tier is **Standard** by default (selectable per thaw: Bulk =
cheapest/slowest, Expedited = 1–5 min, Glacier-Flexible only).

### Where it shows up

- **Explorer → "Change storage class"** (`POST /api/s3/manage/tier`) — archive classic→cold, restore
  cold→classic (thaw then transition), or **restore-only** (thaw to a temporary readable copy without a
  permanent transition). A per-object `tier` job (`internal/job/tier.go`) on its own low-cost lane, with
  a live progress strip. Archived rows are badged `❄ GLACIER` and their download becomes a Restore
  action. Class choices carry an inline explanation of each tier.
- **Process / download from Glacier** — a full-S3 run, or the Import-from-S3 download, whose inputs are
  archived initiates the thaw and parks (`internal/job/storage.go`; the whole-folder pull and the
  low-disk stager both thaw-gate); on resume it re-pulls and stacks once readable. This is
  "download and start processing from Glacier data" — as one visitable task.
- **Backups** — pick a storage class in the Backup panel to archive the heavy components (db dump,
  library tar, atlas); the **manifest and app-state stay instant** so the backup picker and browser
  restore keep working without a thaw. A restore thaws first.
- **Library mirror** — a matched master that is archived kicks off its restore and the run falls back to
  a local rebuild (never blocks); a later run finds it warm.
- **Serving** — a freed-then-archived preview/result replies **409 `{archived, pending}`** rather than a
  broken image, so the UI can offer a restore.
- **Connection default class** — optional per-connection write class (instant only; an archived default
  is rejected — the pipeline's own `run.json`/manifests must stay readable).

**Cost note:** `GLACIER`/`GLACIER_IR` bill a 90-day, `DEEP_ARCHIVE` a 180-day minimum-storage
duration; retiering sooner incurs an early-delete charge — so the UI avoids programmatic churn and
never auto-thaws on an interactive preview (only in an explicit, resumable job).

## Safety properties worth knowing

- Credentials: UI secrets encrypted at rest, never echoed; env credentials never logged.
- Deletes: only after per-file S3 verification; aborted folder-wide otherwise.
- Serving: output URLs transparently fall back to the mirror when the local file was freed (or reply
  409 when the mirror object is archived).
- Pool hygiene: a catalogued frame whose file was freed is skipped from master pools with a
  counted warning (never a dangling-symlink stack failure).
- Glacier: class changes preserve the object key + content MD5; archived inputs/results never fail a
  run — they thaw as a durable task or fall back; unsupported endpoints soft-fail cleanly.
