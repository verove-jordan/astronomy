package s3store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/minio/minio-go/v7"
)

// Glacier / storage-class support. AstroStack threads an S3 storage class through every S3 path so
// captures, results and backups can live on cheap cold tiers and be thawed on demand. ONE rule drives
// every branch: an ARCHIVED object (Glacier Flexible Retrieval / Deep Archive) cannot be read
// (GetObject) or server-side-copied until it is RESTORED — a HEAD/Stat still works — whereas an INSTANT
// object (including the confusingly-named GLACIER_IR "Glacier Instant Retrieval") is always immediately
// readable and copyable. Never string-match "glacier": GLACIER_IR is instant, GLACIER is not.
//
// Canonical AWS class names. "" means STANDARD — S3 omits the class on standard objects, so a List/Stat
// of a hot object reports an empty StorageClass, which every predicate here treats as STANDARD.
const (
	ClassStandard    = "STANDARD"
	ClassStandardIA  = "STANDARD_IA"
	ClassOneZoneIA   = "ONEZONE_IA"
	ClassIntelligent = "INTELLIGENT_TIERING"
	ClassGlacierIR   = "GLACIER_IR"   // Glacier Instant Retrieval — archival price, INSTANT reads (no thaw)
	ClassGlacier     = "GLACIER"      // Glacier Flexible Retrieval — must be restored before read/copy
	ClassDeepArchive = "DEEP_ARCHIVE" // coldest; must be restored before read/copy (up to 48 h)
)

// IsArchivedClass reports whether a storage class requires a restore before its objects can be read or
// copied. Only GLACIER (Flexible) and DEEP_ARCHIVE are archived; GLACIER_IR is INSTANT despite its name.
func IsArchivedClass(class string) bool {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case ClassGlacier, ClassDeepArchive:
		return true
	}
	return false
}

// IsInstantClass reports whether a storage class is immediately readable and copyable — the complement of
// IsArchivedClass over the classes we recognize. "" (== STANDARD) is instant.
func IsInstantClass(class string) bool { return !IsArchivedClass(class) }

// ValidTargetClass reports whether class is a storage class we can transition an object TO (the whitelist
// the API validates a tier request against). "" is rejected — a transition target must be explicit.
func ValidTargetClass(class string) bool {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case ClassStandard, ClassStandardIA, ClassOneZoneIA, ClassIntelligent,
		ClassGlacierIR, ClassGlacier, ClassDeepArchive:
		return true
	}
	return false
}

// Archived reports whether this object currently sits in an archived class (needs a restore to read/copy).
func (o Object) Archived() bool { return IsArchivedClass(o.StorageClass) }

// RestorePending reports whether a thaw for this object is in progress (the temporary copy is not ready
// yet). Only meaningful on a Stat result — a List entry never carries restore status.
func (o Object) RestorePending() bool { return o.Restore != nil && o.Restore.Ongoing }

// RestoreReady reports whether an archived object has a completed, not-yet-expired restore — its bytes are
// temporarily readable until Restore.ExpiryMs. Only meaningful on a Stat result.
func (o Object) RestoreReady() bool {
	return o.Restore != nil && !o.Restore.Ongoing && o.Restore.ExpiryMs != 0
}

// RestoreTier selects the retrieval speed/cost of a thaw. Standard (~3–5 h Flexible / ~12 h Deep Archive)
// is the balanced default; Bulk is cheapest and slowest (up to 48 h); Expedited is 1–5 min but
// Glacier-Flexible-only and the priciest.
type RestoreTier string

const (
	TierStandard  RestoreTier = "Standard"
	TierBulk      RestoreTier = "Bulk"
	TierExpedited RestoreTier = "Expedited"
)

// ParseRestoreTier maps a free-text tier (from the UI/job) to a RestoreTier, defaulting to Standard for
// anything unrecognized (the user-confirmed default).
func ParseRestoreTier(s string) RestoreTier {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "bulk":
		return TierBulk
	case "expedited":
		return TierExpedited
	default:
		return TierStandard
	}
}

func (t RestoreTier) minioTier() minio.TierType {
	switch t {
	case TierBulk:
		return minio.TierBulk
	case TierExpedited:
		return minio.TierExpedited
	default:
		return minio.TierStandard
	}
}

// Readiness answers "can I GET this object right now?".
type Readiness int

const (
	Readable    Readiness = iota // instant class, or an archived object whose restore has completed
	Pending                      // archived, a restore is in progress — not readable yet
	NeedsRestore                 // archived, no restore requested — a thaw must be initiated first
)

// ErrRestoreUnsupported is returned by Restore when the endpoint does not implement the S3 restore API
// (MinIO and some S3 gateways have no Glacier). Callers soft-fail — a run never dies because the endpoint
// cannot archive/thaw; the class controls simply do nothing.
var ErrRestoreUnsupported = errors.New("s3: endpoint does not support object restore (no Glacier)")

// defaultRestoreDays is the temporary-copy lifetime a thaw requests when the caller passes 0 — long enough
// to pull/reprocess an archived capture without re-thawing, short enough to limit standard-storage charges.
const defaultRestoreDays = 7

// copySelfMaxBytes is the single-part CopyObject ceiling (S3's 5 GiB limit); a larger object's in-place
// class change goes through ComposeObject (a multipart server-side copy) instead.
const copySelfMaxBytes = 5 << 30

// Restore initiates a thaw of an archived object into a temporarily-readable copy (readable for `days`,
// default defaultRestoreDays). It is IDEMPOTENT: a restore already in progress (409
// RestoreAlreadyInProgress) or already completed is treated as success, so it is safe to call on every
// poll and after an engine restart. An endpoint without the restore API returns ErrRestoreUnsupported.
func (c *Client) Restore(ctx context.Context, bucket, key string, days int, tier RestoreTier) error {
	if days < 1 {
		days = defaultRestoreDays
	}
	req := minio.RestoreRequest{}
	req.SetDays(days)
	req.SetGlacierJobParameters(minio.GlacierJobParameters{Tier: tier.minioTier()})
	err := c.mc.RestoreObject(ctx, bucket, key, "", req)
	if err == nil || isRestoreInProgress(err) {
		return nil
	}
	if IsRestoreUnsupported(err) {
		return ErrRestoreUnsupported
	}
	return fmt.Errorf("s3 restore %s: %w", key, err)
}

// Readiness reports whether bucket/key can be read now, using a Stat (HEAD works even on archived
// objects). An instant class → Readable; an archived object → Readable once its restore completes (and
// before it expires), Pending while a restore is in flight, else NeedsRestore.
func (c *Client) Readiness(ctx context.Context, bucket, key string) (Readiness, error) {
	obj, ok, err := c.Stat(ctx, bucket, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("s3 readiness %s: object not found", key)
	}
	switch {
	case !obj.Archived():
		return Readable, nil
	case obj.RestoreReady():
		return Readable, nil
	case obj.RestorePending():
		return Pending, nil
	default:
		return NeedsRestore, nil
	}
}

// ChangeStorageClass rewrites an object's storage class IN PLACE by server-side-copying it onto its own
// key with the new class. It PRESERVES the object's user metadata (notably the Astro-Md5 that
// remove-local's strong verification and re-uploads rely on) and its content type — a metadata-replacing
// copy that dropped them would break MD5 verification and served MIME types for every transitioned file.
// The source must be instant-readable: copying an ARCHIVED source fails with InvalidObjectState, so the
// caller thaws first (see Readiness/Restore). Objects larger than copySelfMaxBytes (5 GiB) go through
// ComposeObject (multipart server-side copy) instead of the single-part CopyObject.
func (c *Client) ChangeStorageClass(ctx context.Context, bucket, key, class string) error {
	info, err := c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 stat %s: %w", key, err)
	}
	// Carry forward existing user metadata + content-type; ReplaceMetadata would otherwise wipe them.
	// minio's CopyDestOptions.Marshal recognizes "Content-Type" and "X-Amz-Storage-Class" as standard
	// headers (set directly), and prefixes everything else with x-amz-meta- — so Astro-Md5 round-trips.
	meta := make(map[string]string, len(info.UserMetadata)+2)
	for k, v := range info.UserMetadata {
		meta[k] = v
	}
	if info.ContentType != "" {
		meta["Content-Type"] = info.ContentType
	}
	meta["X-Amz-Storage-Class"] = strings.ToUpper(strings.TrimSpace(class))

	dst := minio.CopyDestOptions{Bucket: bucket, Object: key, UserMetadata: meta, ReplaceMetadata: true}
	src := minio.CopySrcOptions{Bucket: bucket, Object: key}
	err = withRetry(ctx, "change-class", func() error {
		var e error
		if info.Size > copySelfMaxBytes {
			_, e = c.mc.ComposeObject(ctx, dst, src)
		} else {
			_, e = c.mc.CopyObject(ctx, dst, src)
		}
		return e
	})
	if err != nil {
		return fmt.Errorf("s3 change-class %s -> %s: %w", key, class, err)
	}
	return nil
}

// IsArchivedReadErr reports whether err is S3's InvalidObjectState — a GET/copy of an archived object that
// has not been restored. Callers pre-flight with Readiness to avoid this, but it is the authoritative
// signal if one slips through.
func IsArchivedReadErr(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "InvalidObjectState"
	}
	return false
}

// isRestoreInProgress reports whether err says a restore is already underway (409 RestoreAlreadyInProgress)
// — treated as success by Restore so repeated/racing thaw requests are harmless.
func isRestoreInProgress(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "RestoreAlreadyInProgress" || resp.StatusCode == http.StatusConflict
	}
	return false
}

// IsRestoreUnsupported reports whether err says the endpoint has no restore/Glacier support (501
// NotImplemented / 405 MethodNotAllowed) — so callers soft-fail instead of looping forever.
func IsRestoreUnsupported(err error) bool {
	if errors.Is(err, ErrRestoreUnsupported) {
		return true
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.StatusCode {
		case http.StatusNotImplemented, http.StatusMethodNotAllowed:
			return true
		}
		switch resp.Code {
		case "NotImplemented", "MethodNotAllowed":
			return true
		}
	}
	return false
}
