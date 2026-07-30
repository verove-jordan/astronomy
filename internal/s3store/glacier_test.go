package s3store

import (
	"fmt"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The class families drive every Glacier branch, so the "" == STANDARD and GLACIER_IR-is-instant rules
// must hold across casing/whitespace — a wrong classification would archive-lock or wrongly-thaw objects.
func TestClassFamilies(t *testing.T) {
	archived := []string{"GLACIER", "DEEP_ARCHIVE", "glacier", " deep_archive "}
	for _, c := range archived {
		assert.True(t, IsArchivedClass(c), "archived: %q", c)
		assert.False(t, IsInstantClass(c), "not instant: %q", c)
	}
	instant := []string{"", "STANDARD", "STANDARD_IA", "ONEZONE_IA", "INTELLIGENT_TIERING", "GLACIER_IR", "glacier_ir"}
	for _, c := range instant {
		assert.False(t, IsArchivedClass(c), "not archived: %q", c)
		assert.True(t, IsInstantClass(c), "instant: %q", c)
	}
}

func TestValidTargetClass(t *testing.T) {
	for _, c := range []string{"STANDARD", "GLACIER", "DEEP_ARCHIVE", "GLACIER_IR", "STANDARD_IA", "glacier"} {
		assert.True(t, ValidTargetClass(c), "valid: %q", c)
	}
	for _, c := range []string{"", "  ", "COLD", "nonsense"} {
		assert.False(t, ValidTargetClass(c), "invalid: %q", c)
	}
}

func TestObjectRestorePredicates(t *testing.T) {
	// A hot object is Readable-eligible and never pending/ready.
	hot := Object{StorageClass: ""}
	assert.False(t, hot.Archived())
	assert.False(t, hot.RestorePending())
	assert.False(t, hot.RestoreReady())

	// Archived, no restore requested → archived, neither pending nor ready (needs a thaw).
	cold := Object{StorageClass: "GLACIER"}
	assert.True(t, cold.Archived())
	assert.False(t, cold.RestorePending())
	assert.False(t, cold.RestoreReady())

	// Archived, thaw in progress → pending.
	thawing := Object{StorageClass: "DEEP_ARCHIVE", Restore: &RestoreState{Ongoing: true}}
	assert.True(t, thawing.RestorePending())
	assert.False(t, thawing.RestoreReady())

	// Archived, restore completed with an expiry → ready.
	ready := Object{StorageClass: "GLACIER", Restore: &RestoreState{Ongoing: false, ExpiryMs: 1_700_000_000_000}}
	assert.False(t, ready.RestorePending())
	assert.True(t, ready.RestoreReady())

	// GLACIER_IR is instant despite the name — never "archived", so it needs no restore path.
	ir := Object{StorageClass: "GLACIER_IR"}
	assert.False(t, ir.Archived())
}

func TestParseRestoreTier(t *testing.T) {
	assert.Equal(t, TierStandard, ParseRestoreTier(""))
	assert.Equal(t, TierStandard, ParseRestoreTier("Standard"))
	assert.Equal(t, TierStandard, ParseRestoreTier("weird"))
	assert.Equal(t, TierBulk, ParseRestoreTier("bulk"))
	assert.Equal(t, TierBulk, ParseRestoreTier(" BULK "))
	assert.Equal(t, TierExpedited, ParseRestoreTier("Expedited"))
}

// The error classifiers gate correctness: a mis-classified InvalidObjectState would fail a run instead of
// thawing; a missed RestoreAlreadyInProgress would surface a spurious error on a harmless re-request.
func TestGlacierErrorClassifiers(t *testing.T) {
	invalidState := minio.ErrorResponse{Code: "InvalidObjectState", StatusCode: 403}
	inProgress := minio.ErrorResponse{Code: "RestoreAlreadyInProgress", StatusCode: 409}
	notImpl := minio.ErrorResponse{Code: "NotImplemented", StatusCode: 501}
	notFound := minio.ErrorResponse{Code: "NoSuchKey", StatusCode: 404}

	assert.True(t, IsArchivedReadErr(invalidState))
	assert.True(t, IsArchivedReadErr(fmt.Errorf("s3 download k: %w", invalidState)), "unwraps a wrapped error")
	assert.False(t, IsArchivedReadErr(notFound))
	assert.False(t, IsArchivedReadErr(nil))

	assert.True(t, isRestoreInProgress(inProgress))
	assert.True(t, isRestoreInProgress(minio.ErrorResponse{StatusCode: 409}), "409 alone is in-progress")
	assert.False(t, isRestoreInProgress(invalidState))

	assert.True(t, IsRestoreUnsupported(notImpl))
	assert.True(t, IsRestoreUnsupported(minio.ErrorResponse{StatusCode: 405}))
	assert.True(t, IsRestoreUnsupported(ErrRestoreUnsupported), "the sentinel classifies as unsupported")
	assert.True(t, IsRestoreUnsupported(fmt.Errorf("wrap: %w", ErrRestoreUnsupported)))
	assert.False(t, IsRestoreUnsupported(notFound))
}

// New must refuse an ARCHIVED default class — a pipeline that wrote run.json/manifests to Glacier would
// make its own control files unreadable (InvalidObjectState) and break every run.
func TestNewRejectsArchivedDefaultClass(t *testing.T) {
	base := Config{Endpoint: "s3.amazonaws.com", AccessKeyID: "k", SecretKey: "s"}

	for _, bad := range []string{"GLACIER", "DEEP_ARCHIVE", "glacier"} {
		cfg := base
		cfg.DefaultStorageClass = bad
		_, err := New(cfg)
		assert.Error(t, err, "archived default %q rejected", bad)
	}

	for _, ok := range []string{"", "STANDARD", "GLACIER_IR", "STANDARD_IA"} {
		cfg := base
		cfg.DefaultStorageClass = ok
		c, err := New(cfg)
		require.NoError(t, err, "instant default %q accepted", ok)
		assert.Equal(t, ok == "" || ok == "STANDARD" || ok == "GLACIER_IR" || ok == "STANDARD_IA", true)
		_ = c
	}
}
