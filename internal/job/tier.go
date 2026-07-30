package job

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

// runTier performs a storage-class change of the requested objects/folders on the explorer's chosen
// connection: archive an instant class to Glacier/Deep-Archive, or thaw an archived object (and, unless
// RestoreOnly, make the thaw permanent by rewriting it to the target class). It mirrors runS3Move — plan
// the object set, then a per-object loop with the same throttled byte-progress stream — but the per-object
// action is ChangeStorageClass / Restore instead of copy+rekey+delete. Because a class change keeps the
// SAME key and size, the s3_objects ledger is untouched (unlike move).
//
// Archived sources cannot be copied until restored, so an archived object that is not yet readable has its
// thaw initiated (idempotently) and is collected into a waiting set; a non-empty waiting set returns
// *thawWaiting, which run() turns into a causeThaw pause. The auto-resume sweep re-enters this function on a
// thaw cadence; each pass re-enumerates from S3 (the source of truth), transitions the now-ready objects
// and re-collects the still-cold ones, until all are done — restart-safe and idempotent.
func (m *Manager) runTier(ctx context.Context, id int64, tr *TierRequest) (any, error) {
	if m.s3conn == nil {
		return nil, fmt.Errorf("s3 tier: connections require encryption (set ASTRO_ENCRYPTION_KEY)")
	}
	if tr.Bucket == "" || len(tr.Srcs) == 0 {
		return nil, fmt.Errorf("s3 tier: bucket and at least one source are required")
	}
	if !tr.RestoreOnly && !s3store.ValidTargetClass(tr.TargetClass) {
		return nil, fmt.Errorf("s3 tier: invalid target storage class %q", tr.TargetClass)
	}
	client, err := m.s3conn.ClientFor(ctx, tr.Conn)
	if err != nil {
		return nil, fmt.Errorf("s3 tier: connection %d: %w", tr.Conn, err)
	}

	plan, err := m.planTier(ctx, client, tr)
	if err != nil {
		return nil, err
	}

	verb := tierVerb(tr)
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: verb + " scanning…"})
	onProg := m.newTransferProgress(ctx, id, verb)
	gate := m.gateFor(id)

	tier := s3store.ParseRestoreTier(tr.Tier)
	var waiting []string
	var doneBytes int64
	for i, it := range plan.items {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if gate != nil && gate.requested() {
			return nil, transfer.ErrPaused // parked as a resumable manual pause; resume re-enumerates
		}
		act, err := m.applyTier(ctx, client, tr, it, tier)
		if err != nil {
			return nil, err
		}
		if act == tierWaiting {
			waiting = append(waiting, it.key)
		}
		doneBytes += it.size
		onProg(transfer.Progress{Name: it.key, Files: i + 1, TotalFiles: len(plan.items),
			BytesDone: doneBytes, BytesTotal: plan.totalBytes})
	}
	if len(waiting) > 0 {
		return nil, &thawWaiting{keys: waiting}
	}
	return transfer.Result{Op: "tier", Files: len(plan.items), Bytes: plan.totalBytes}, nil
}

// thawWaiting is returned by runTier (and the pull path) when some objects are still restoring — run()
// parks the job as a causeThaw pause instead of failing it.
type thawWaiting struct{ keys []string }

func (e *thawWaiting) Error() string {
	return fmt.Sprintf("waiting for Glacier restore of %d object(s)", len(e.keys))
}

// tierAction is what applyTier did to one object.
type tierAction int

const (
	tierSkip    tierAction = iota // already at the target class → nothing to do
	tierDone                      // transitioned (or restored, for RestoreOnly) this pass
	tierWaiting                   // archived + restore not ready → thaw initiated, must poll again
)

// applyTier applies the per-object action: for an archived source it first ensures a thaw is in flight
// (ensureThaw, which also yields readiness), then decideTier picks the action, and a tierDone that is not
// restore-only performs the in-place class change. The decision matrix lives in the pure decideTier so it
// is unit-testable without a live client.
func (m *Manager) applyTier(ctx context.Context, client *s3store.Client, tr *TierRequest, it tierItem, tier s3store.RestoreTier) (tierAction, error) {
	rd := s3store.Readable // instant sources are always readable
	if s3store.IsArchivedClass(it.class) {
		var err error
		if rd, err = m.ensureThaw(ctx, client, tr.Bucket, it.key, tr.Days, tier); err != nil {
			return 0, err
		}
	}
	act := decideTier(it.class, tr.TargetClass, tr.RestoreOnly, rd)
	if act == tierDone && !tr.RestoreOnly {
		if err := client.ChangeStorageClass(ctx, tr.Bucket, it.key, tr.TargetClass); err != nil {
			return 0, err
		}
	}
	return act, nil
}

// decideTier is the pure current-class × target × restore-readiness decision matrix: what to do with one
// object. Split from applyTier so the (subtle) Glacier state machine is unit-tested without any S3 I/O.
//   - already at target (and not a forced restore) → skip
//   - instant source, restore-only → skip (nothing to thaw); instant → target → transition (done)
//   - archived source, not readable → wait (a thaw was initiated); readable → done (transition unless restore-only)
func decideTier(currentClass, targetClass string, restoreOnly bool, rd s3store.Readiness) tierAction {
	if !restoreOnly && sameClass(currentClass, targetClass) {
		return tierSkip
	}
	if !s3store.IsArchivedClass(currentClass) {
		if restoreOnly {
			return tierSkip
		}
		return tierDone
	}
	if rd != s3store.Readable {
		return tierWaiting
	}
	return tierDone
}

// beginThaw kicks off a Glacier restore for each archived input key on the pipeline's resolved connection,
// at the default retrieval tier (Standard). It is idempotent (safe to re-call every poll) and is how a
// full-S3 run that hit cold inputs starts the thaw before parking. It returns a hard error only when the
// endpoint has no Glacier support at all (so the caller fails the run cleanly rather than polling for 48 h);
// a transient per-key failure is logged and simply retried on the next poll.
func (m *Manager) beginThaw(ctx context.Context, id int64, bucket string, keys []string) error {
	client, err := m.s3Client(ctx)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, terr := m.ensureThaw(ctx, client, bucket, key, 0, s3store.TierStandard); terr != nil {
			if errors.Is(terr, s3store.ErrRestoreUnsupported) {
				return fmt.Errorf("cannot process archived inputs: this S3 endpoint does not support Glacier restore")
			}
			log.Printf("astrostack: job %d thaw %s: %v", id, key, terr)
		}
	}
	m.publish(Event{JobID: id, Status: store.JobRunning,
		Line: fmt.Sprintf("❄ thawing %d archived input(s) from Glacier (Standard tier)…", len(keys)), Ts: time.Now().UnixMilli()})
	return nil
}

// ensureThaw is the single idempotent thaw chokepoint (reused by the process-from-Glacier pull and backup
// restore): it reports readiness and, when an object is archived with no restore yet, initiates one. A
// just-initiated restore returns Pending. An endpoint without Glacier surfaces ErrRestoreUnsupported.
func (m *Manager) ensureThaw(ctx context.Context, client *s3store.Client, bucket, key string, days int, tier s3store.RestoreTier) (s3store.Readiness, error) {
	rd, err := client.Readiness(ctx, bucket, key)
	if err != nil {
		return 0, err
	}
	if rd == s3store.NeedsRestore {
		if err := client.Restore(ctx, bucket, key, days, tier); err != nil {
			return 0, err
		}
		return s3store.Pending, nil // just kicked off — not readable yet this pass
	}
	return rd, nil
}

// tierItem is one object a tier job acts on: its key, size (progress denominator) and current class.
type tierItem struct {
	key   string
	size  int64
	class string
}

// tierPlan is the full object set a tier job will process plus its summed size.
type tierPlan struct {
	items      []tierItem
	totalBytes int64
}

// planTier enumerates every object across all sources with its current class (a folder key is expanded via
// List — which carries the class; a single object is Stat'd). The class drives applyTier's decision, and
// the summed size gives the progress bar an accurate denominator before the first action.
func (m *Manager) planTier(ctx context.Context, client *s3store.Client, tr *TierRequest) (tierPlan, error) {
	var plan tierPlan
	for _, src := range tr.Srcs {
		if src == "" {
			continue
		}
		if strings.HasSuffix(src, "/") {
			objs, err := client.List(ctx, tr.Bucket, src)
			if err != nil {
				return tierPlan{}, fmt.Errorf("s3 tier: list %s: %w", src, err)
			}
			for _, o := range objs {
				plan.items = append(plan.items, tierItem{key: o.Key, size: o.Size, class: o.StorageClass})
				plan.totalBytes += o.Size
			}
			continue
		}
		obj, ok, err := client.Stat(ctx, tr.Bucket, src)
		if err != nil {
			return tierPlan{}, fmt.Errorf("s3 tier: stat %s: %w", src, err)
		}
		if !ok {
			continue // source vanished between listing and action — skip it
		}
		plan.items = append(plan.items, tierItem{key: src, size: obj.Size, class: obj.StorageClass})
		plan.totalBytes += obj.Size
	}
	return plan, nil
}

// sameClass reports whether two storage-class strings denote the same class, treating "" as STANDARD.
func sameClass(a, b string) bool {
	na := strings.ToUpper(strings.TrimSpace(a))
	nb := strings.ToUpper(strings.TrimSpace(b))
	if na == "" {
		na = s3store.ClassStandard
	}
	if nb == "" {
		nb = s3store.ClassStandard
	}
	return na == nb
}

// tierVerb is the progress-bar label for the kind of tier operation (drives the Tasks view text).
func tierVerb(tr *TierRequest) string {
	switch {
	case tr.RestoreOnly:
		return "Restoring"
	case s3store.IsArchivedClass(tr.TargetClass):
		return "Archiving"
	default:
		return "Changing storage class"
	}
}
