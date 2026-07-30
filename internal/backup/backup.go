// Package backup snapshots AstroStack's precious local state — the Postgres database (every session,
// output and reusable master), the calibration-master library, the light-pollution atlas, and the
// browser-only app state (favorites/setups/prefs + AI-agent chats, exported by the UI) — to an S3
// "backup/<stamp>/" folder, and restores it. Big captures/results are NOT here: those are handled by the
// per-folder sync/transfer and full-S3 run modes. Components each soft-fail on backup (a partial snapshot
// still succeeds); the manifest records what was actually stored. Credentials are env-only (Postgres via
// DatabaseURL, S3 on the s3store.Client) — never carried in the request.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// Component identifiers a snapshot can include.
const (
	CompDB       = "db"       // Postgres logical dump (pg_dump -Fc)
	CompLibrary  = "library"  // calibration master library (tar of LibraryDir)
	CompAtlas    = "atlas"    // light-pollution atlas (atlas.bin + atlas.json)
	CompAppState = "appstate" // browser-only state (favorites/setups/prefs + AI chats), exported by the UI
)

// AllComponents is the default component set, most-precious first.
var AllComponents = []string{CompDB, CompLibrary, CompAtlas, CompAppState}

// Config carries the local sources/tools a snapshot reads (paths resolved by the caller). Postgres
// credentials live in DatabaseURL; the S3 credentials are on the s3store.Client (env-only).
type Config struct {
	DatabaseURL  string
	LibraryDir   string
	AtlasBin     string // "" or absent → the atlas component is skipped
	AtlasJSON    string
	WorkDir      string // scratch for the pg dump / library tar before upload
	PGDumpBin    string // default "pg_dump"
	PGRestoreBin string // default "pg_restore"
}

// Manifest records what a snapshot contains and the absolute roots it was taken from, so a restore can
// warn on mismatched roots (cross-root remap is a documented follow-up). Timestamps are int64 ms.
type Manifest struct {
	StampMs    int64    `json:"stamp_ms"`
	Stamp      string   `json:"stamp"` // the backup/<stamp>/ folder name
	Components []string `json:"components"`
	LibraryDir string   `json:"library_dir,omitempty"`
	AtlasDir   string   `json:"atlas_dir,omitempty"`
}

func logf(onLog func(string), format string, a ...any) {
	if onLog != nil {
		onLog(fmt.Sprintf(format, a...))
	}
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func (c Config) pgDump() string {
	if c.PGDumpBin != "" {
		return c.PGDumpBin
	}
	return "pg_dump"
}

func (c Config) pgRestore() string {
	if c.PGRestoreBin != "" {
		return c.PGRestoreBin
	}
	return "pg_restore"
}

// Snapshot writes the selected components under keyPrefix/ (keyPrefix already includes backup/<stamp>) and
// uploads a manifest that lists what was actually stored. Each component soft-fails (logged, skipped) so a
// partial snapshot still succeeds. Only the manifest upload failing (the folder marker) is fatal.
func Snapshot(ctx context.Context, client *s3store.Client, bucket, keyPrefix string, man Manifest, comps []string, appstate string, cfg Config, onLog func(string)) (Manifest, error) {
	scratch := filepath.Join(cfg.WorkDir, "backup", man.Stamp)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return man, fmt.Errorf("backup scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	want := toSet(comps)

	if want[CompDB] {
		logf(onLog, "Backing up database…")
		if err := snapshotDB(ctx, client, bucket, keyPrefix, scratch, cfg); err != nil {
			logf(onLog, "database: skipped (%v)", err)
		} else {
			man.Components = append(man.Components, CompDB)
		}
	}
	if want[CompLibrary] {
		logf(onLog, "Archiving calibration library…")
		if n, err := snapshotLibrary(ctx, client, bucket, keyPrefix, scratch, cfg); err != nil {
			logf(onLog, "library: skipped (%v)", err)
		} else if n == 0 {
			logf(onLog, "library: empty, skipped")
		} else {
			man.Components = append(man.Components, CompLibrary)
			man.LibraryDir = cfg.LibraryDir
		}
	}
	if want[CompAtlas] {
		logf(onLog, "Backing up light-pollution atlas…")
		if err := snapshotAtlas(ctx, client, bucket, keyPrefix, cfg); err != nil {
			logf(onLog, "atlas: skipped (%v)", err)
		} else {
			man.Components = append(man.Components, CompAtlas)
			man.AtlasDir = filepath.Dir(cfg.AtlasBin)
		}
	}
	if want[CompAppState] && appstate != "" {
		logf(onLog, "Backing up app settings & AI chats…")
		if err := client.PutBytes(ctx, bucket, keyPrefix+"/appstate.json", []byte(appstate)); err != nil {
			logf(onLog, "appstate: skipped (%v)", err)
		} else {
			man.Components = append(man.Components, CompAppState)
		}
	}

	// Manifest last — its presence marks a complete backup folder (List keys on it).
	mb, _ := json.MarshalIndent(man, "", "  ")
	if err := client.PutBytes(ctx, bucket, keyPrefix+"/manifest.json", mb); err != nil {
		return man, fmt.Errorf("upload manifest: %w", err)
	}
	logf(onLog, "Backup complete: %s", strings.Join(man.Components, ", "))
	return man, nil
}

func snapshotDB(ctx context.Context, client *s3store.Client, bucket, keyPrefix, scratch string, cfg Config) error {
	if cfg.DatabaseURL == "" {
		return errors.New("no DATABASE_URL")
	}
	if _, err := exec.LookPath(cfg.pgDump()); err != nil {
		return fmt.Errorf("%s not found", cfg.pgDump())
	}
	dump := filepath.Join(scratch, "db.dump")
	cmd := exec.CommandContext(ctx, cfg.pgDump(), "-Fc", "-d", cfg.DatabaseURL, "-f", dump)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_dump: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return client.Upload(ctx, bucket, keyPrefix+"/db.dump", dump, nil)
}

func snapshotLibrary(ctx context.Context, client *s3store.Client, bucket, keyPrefix, scratch string, cfg Config) (int, error) {
	if cfg.LibraryDir == "" {
		return 0, nil
	}
	tarPath := filepath.Join(scratch, "library.tar")
	n, err := tarDir(cfg.LibraryDir, tarPath)
	if err != nil || n == 0 {
		return n, err
	}
	return n, client.Upload(ctx, bucket, keyPrefix+"/library.tar", tarPath, nil)
}

func snapshotAtlas(ctx context.Context, client *s3store.Client, bucket, keyPrefix string, cfg Config) error {
	if cfg.AtlasBin == "" {
		return errors.New("no atlas path")
	}
	if _, err := os.Stat(cfg.AtlasBin); err != nil {
		return fmt.Errorf("atlas not built")
	}
	if err := client.Upload(ctx, bucket, keyPrefix+"/atlas/atlas.bin", cfg.AtlasBin, nil); err != nil {
		return err
	}
	if cfg.AtlasJSON != "" {
		if _, err := os.Stat(cfg.AtlasJSON); err == nil {
			if err := client.Upload(ctx, bucket, keyPrefix+"/atlas/atlas.json", cfg.AtlasJSON, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Restore places the selected components back from keyPrefix/ (keyPrefix includes backup/<stamp>). Each
// component is attempted; failures are collected and returned together so a partial restore is visible.
// The appstate component is applied browser-side (fetched separately), not here.
func Restore(ctx context.Context, client *s3store.Client, bucket, keyPrefix string, comps []string, cfg Config, onLog func(string)) error {
	scratch := filepath.Join(cfg.WorkDir, "restore")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return fmt.Errorf("restore scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	want := toSet(comps)
	var errs []string

	if want[CompDB] {
		logf(onLog, "Restoring database…")
		if err := restoreDB(ctx, client, bucket, keyPrefix, scratch, cfg); err != nil {
			errs = append(errs, "db: "+err.Error())
		}
	}
	if want[CompLibrary] {
		logf(onLog, "Restoring calibration library…")
		if err := restoreLibrary(ctx, client, bucket, keyPrefix, scratch, cfg); err != nil {
			errs = append(errs, "library: "+err.Error())
		}
	}
	if want[CompAtlas] {
		logf(onLog, "Restoring light-pollution atlas…")
		if err := restoreAtlas(ctx, client, bucket, keyPrefix, cfg); err != nil {
			errs = append(errs, "atlas: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	logf(onLog, "Restore complete")
	return nil
}

func restoreDB(ctx context.Context, client *s3store.Client, bucket, keyPrefix, scratch string, cfg Config) error {
	if cfg.DatabaseURL == "" {
		return errors.New("no DATABASE_URL")
	}
	if _, err := exec.LookPath(cfg.pgRestore()); err != nil {
		return fmt.Errorf("%s not found", cfg.pgRestore())
	}
	dump := filepath.Join(scratch, "db.dump")
	if err := client.Download(ctx, bucket, keyPrefix+"/db.dump", dump, nil); err != nil {
		return err
	}
	// --clean --if-exists drops existing objects first so a restore over a live DB is idempotent.
	cmd := exec.CommandContext(ctx, cfg.pgRestore(), "--clean", "--if-exists", "--no-owner", "-d", cfg.DatabaseURL, dump)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func restoreLibrary(ctx context.Context, client *s3store.Client, bucket, keyPrefix, scratch string, cfg Config) error {
	if cfg.LibraryDir == "" {
		return errors.New("no library dir")
	}
	tarPath := filepath.Join(scratch, "library.tar")
	if err := client.Download(ctx, bucket, keyPrefix+"/library.tar", tarPath, nil); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.LibraryDir, 0o755); err != nil {
		return err
	}
	return untar(tarPath, cfg.LibraryDir)
}

func restoreAtlas(ctx context.Context, client *s3store.Client, bucket, keyPrefix string, cfg Config) error {
	if cfg.AtlasBin == "" {
		return errors.New("no atlas path")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.AtlasBin), 0o755); err != nil {
		return err
	}
	if err := client.Download(ctx, bucket, keyPrefix+"/atlas/atlas.bin", cfg.AtlasBin, nil); err != nil {
		return err
	}
	if cfg.AtlasJSON != "" {
		// The sidecar may be absent in older atlases; treat its absence as non-fatal.
		if err := client.Download(ctx, bucket, keyPrefix+"/atlas/atlas.json", cfg.AtlasJSON, nil); err != nil {
			return nil //nolint:nilerr // atlas.bin restored; sidecar optional
		}
	}
	return nil
}

// List returns the manifests of every backup under <userPrefix>/backup/, newest first.
func List(ctx context.Context, client *s3store.Client, bucket, userPrefix string) ([]Manifest, error) {
	prefix := path.Join(userPrefix, "backup") + "/"
	objs, err := client.List(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	var mans []Manifest
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, "/manifest.json") {
			continue
		}
		data, err := client.GetBytes(ctx, bucket, o.Key)
		if err != nil {
			// A manifest that was archived (e.g. the whole backup folder cold-tiered via the explorer) can't
			// be read until thawed — still surface a minimal entry (stamp from the key) so the user sees the
			// backup and can restore it (which thaws it) instead of it silently vanishing from the picker.
			if s3store.IsArchivedReadErr(err) {
				mans = append(mans, Manifest{Stamp: path.Base(path.Dir(o.Key))})
			}
			continue
		}
		var m Manifest
		if json.Unmarshal(data, &m) == nil {
			mans = append(mans, m)
		}
	}
	sort.Slice(mans, func(i, j int) bool { return mans[i].StampMs > mans[j].StampMs })
	return mans, nil
}

// AppState fetches the browser-state JSON stored in one backup (applied UI-side on restore).
func AppState(ctx context.Context, client *s3store.Client, bucket, keyPrefix string) ([]byte, error) {
	return client.GetBytes(ctx, bucket, keyPrefix+"/appstate.json")
}
