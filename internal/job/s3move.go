package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

// runS3Move performs an S3→S3 move of the requested objects/folders on the explorer's chosen connection. For
// each object it does a server-side copy → ledger rekey (so the inspector/serving fallback still resolves the
// moved file) → delete of the source — the same order manageMove used, but per object and as a job. Because a
// server-side copy never routes bytes through the engine, progress is PER OBJECT (each object's whole size
// lands when it completes); the totals are known up front from a listing, so the bar/speed/ETA are accurate.
// It reuses the transfer job's throttled byte-progress stream (newTransferProgress), so the move shows a live
// bar in the explorer and Tasks. A manual pause parks the job (transfer.ErrPaused); a resumed move
// re-enumerates and naturally skips the objects already gone from the source (copy+rekey+delete is per-object
// atomic, so the ledger is never left pointing at a deleted key).
func (m *Manager) runS3Move(ctx context.Context, id int64, mv *MoveRequest) (any, error) {
	if m.s3conn == nil {
		return nil, fmt.Errorf("s3 move: connections require encryption (set ASTRO_ENCRYPTION_KEY)")
	}
	if mv.Bucket == "" || len(mv.Srcs) == 0 {
		return nil, fmt.Errorf("s3 move: bucket and at least one source are required")
	}
	client, err := m.s3conn.ClientFor(ctx, mv.Conn)
	if err != nil {
		return nil, fmt.Errorf("s3 move: connection %d: %w", mv.Conn, err)
	}

	plan, err := m.planMove(ctx, client, mv)
	if err != nil {
		return nil, err
	}

	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "Moving scanning…"})
	onProg := m.newTransferProgress(ctx, id, "Moving")

	gate := m.gateFor(id)
	var doneBytes int64
	for i, it := range plan.items {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if gate != nil && gate.requested() {
			return nil, transfer.ErrPaused // parked as a resumable manual pause; resume re-enumerates
		}
		if err := client.Copy(ctx, mv.Bucket, it.srcKey, it.dstKey); err != nil {
			return nil, fmt.Errorf("s3 move: copy %s: %w", it.srcKey, err)
		}
		// Rekey the ledger BEFORE deleting the source, so a failed delete leaves the ledger already pointing
		// at the surviving copy rather than at a deleted key.
		if _, err := m.store.RekeyS3Objects(ctx, mv.Bucket, it.srcKey, it.dstKey); err != nil {
			return nil, fmt.Errorf("s3 move: rekey ledger %s: %w", it.srcKey, err)
		}
		if err := client.Delete(ctx, mv.Bucket, it.srcKey); err != nil {
			return nil, fmt.Errorf("s3 move: delete source %s: %w", it.srcKey, err)
		}
		doneBytes += it.size
		onProg(transfer.Progress{Name: it.srcKey, Files: i + 1, TotalFiles: len(plan.items),
			BytesDone: doneBytes, BytesTotal: plan.totalBytes})
	}
	return transfer.Result{Op: "move", Files: len(plan.items), Bytes: plan.totalBytes}, nil
}

// moveItem is one object to relocate: its source key, resolved destination key, and size (for the bar).
type moveItem struct {
	srcKey string
	dstKey string
	size   int64
}

// movePlan is the full set of objects a move will relocate plus their summed size (the bar's denominator).
type movePlan struct {
	items      []moveItem
	totalBytes int64
}

// planMove enumerates every object to move across all sources, resolving each object's destination key and
// summing the total bytes — so the progress bar has an accurate denominator before the first copy. A source
// that is a no-op (moved into its own place) is skipped; a folder moved into itself is rejected. A folder
// enumerates its objects (sub-paths preserved under the destination); a single object is Stat'd for its size.
func (m *Manager) planMove(ctx context.Context, client *s3store.Client, mv *MoveRequest) (movePlan, error) {
	var plan movePlan
	for _, src := range mv.Srcs {
		if src == "" {
			continue
		}
		newKey, isDir := s3store.MoveDest(src, mv.Dst)
		if newKey == src {
			continue // no-op move into the same place
		}
		if isDir && strings.HasPrefix(newKey, src) {
			return movePlan{}, fmt.Errorf("s3 move: cannot move a folder into itself (%s)", src)
		}
		if isDir {
			objs, err := client.List(ctx, mv.Bucket, src)
			if err != nil {
				return movePlan{}, fmt.Errorf("s3 move: list %s: %w", src, err)
			}
			for _, o := range objs {
				rel := strings.TrimPrefix(o.Key, src)
				plan.items = append(plan.items, moveItem{srcKey: o.Key, dstKey: newKey + rel, size: o.Size})
				plan.totalBytes += o.Size
			}
			continue
		}
		obj, ok, err := client.Stat(ctx, mv.Bucket, src)
		if err != nil {
			return movePlan{}, fmt.Errorf("s3 move: stat %s: %w", src, err)
		}
		if !ok {
			continue // source vanished between listing and move — skip it
		}
		plan.items = append(plan.items, moveItem{srcKey: src, dstKey: newKey, size: obj.Size})
		plan.totalBytes += obj.Size
	}
	return plan, nil
}
