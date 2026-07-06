package job

import (
	"context"
	"path"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/backup"
	"github.com/verove-jordan/astronomy/internal/store"
)

// backupConfig builds the backup.Config from the engine config, resolving the light-pollution atlas paths
// the same way the provider does (<DataDir>/lightpollution/atlas.bin unless ASTRO_LIGHTPOLLUTION_ATLAS
// overrides it). Postgres/S3 credentials come from the environment, never the request.
func (m *Manager) backupConfig() backup.Config {
	atlasBin := m.cfg.LightPollutionAtlas
	if atlasBin == "" {
		atlasBin = filepath.Join(m.cfg.DataDir, "lightpollution", "atlas.bin")
	}
	return backup.Config{
		DatabaseURL: m.cfg.DatabaseURL,
		LibraryDir:  m.cfg.LibraryDir,
		AtlasBin:    atlasBin,
		AtlasJSON:   filepath.Join(filepath.Dir(atlasBin), "atlas.json"),
		WorkDir:     m.cfg.WorkDir,
	}
}

// backupStamp formats a backup folder name from an int64-ms timestamp (UTC, filename-safe).
func backupStamp(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("20060102T150405Z")
}

// progressPublisher returns an onLog callback that streams each backup/restore step through the job event
// stream (a log line + a coarse ramping percentage, since the component count is small and their sizes
// vary wildly). Never reaches 100 while running — the run() "done" event owns that.
func (m *Manager) progressPublisher(ctx context.Context, id int64, step int) func(string) {
	pct := 0
	return func(msg string) {
		if pct < 95 {
			pct += step
			if pct > 95 {
				pct = 95
			}
		}
		_ = m.store.UpdateJobProgress(ctx, id, pct, msg, "")
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: msg,
			Line: msg, Ts: time.Now().UnixMilli()})
	}
}

// runBackup snapshots the selected components to <prefix>/backup/<stamp>/ on S3, streaming step progress.
func (m *Manager) runBackup(ctx context.Context, id int64, br *BackupRequest) (any, error) {
	client, err := m.s3Client(ctx)
	if err != nil {
		return nil, err
	}
	stamp := backupStamp(br.StampMs)
	keyPrefix := path.Join(br.Prefix, "backup", stamp)
	comps := br.Components
	if len(comps) == 0 {
		comps = backup.AllComponents
	}

	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "backup starting"})
	man := backup.Manifest{StampMs: br.StampMs, Stamp: stamp}
	res, err := backup.Snapshot(ctx, client, br.Bucket, keyPrefix, man, comps, br.AppState, m.backupConfig(),
		m.progressPublisher(ctx, id, 15))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// runRestore restores the selected components from <prefix>/backup/<stamp>/ on S3.
func (m *Manager) runRestore(ctx context.Context, id int64, rr *RestoreRequest) (any, error) {
	client, err := m.s3Client(ctx)
	if err != nil {
		return nil, err
	}
	keyPrefix := path.Join(rr.Prefix, "backup", rr.Stamp)
	comps := rr.Components
	if len(comps) == 0 {
		comps = backup.AllComponents
	}

	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "restore starting"})
	if err := backup.Restore(ctx, client, rr.Bucket, keyPrefix, comps, m.backupConfig(),
		m.progressPublisher(ctx, id, 25)); err != nil {
		return nil, err
	}
	return map[string]any{"restored": comps, "stamp": rr.Stamp}, nil
}
