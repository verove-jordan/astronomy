package job

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// decideTier is the Glacier state machine — the subtle part of the tier job. It must: skip a no-op, do a
// direct change for an instant source, and for an archived source wait until restored then transition
// (unless restore-only, which stops at "readable"). "" is STANDARD; GLACIER_IR is instant.
func TestDecideTier(t *testing.T) {
	cases := []struct {
		name        string
		current     string
		target      string
		restoreOnly bool
		rd          s3store.Readiness
		want        tierAction
	}{
		{"standard→glacier (archive)", "STANDARD", "GLACIER", false, s3store.Readable, tierDone},
		{"empty(standard)→glacier", "", "GLACIER", false, s3store.Readable, tierDone},
		{"already glacier, no-op", "GLACIER", "GLACIER", false, s3store.Readable, tierSkip},
		{"already standard, no-op (empty)", "", "STANDARD", false, s3store.Readable, tierSkip},
		{"glacier_ir is instant → direct change", "GLACIER_IR", "STANDARD", false, s3store.Readable, tierDone},
		{"instant→glacier_ir direct", "STANDARD", "GLACIER_IR", false, s3store.Readable, tierDone},
		// Archived source needs restore first.
		{"glacier→standard, still thawing", "GLACIER", "STANDARD", false, s3store.Pending, tierWaiting},
		{"glacier→standard, needs restore", "GLACIER", "STANDARD", false, s3store.NeedsRestore, tierWaiting},
		{"glacier→standard, restored → transition", "GLACIER", "STANDARD", false, s3store.Readable, tierDone},
		{"deep_archive→glacier, thawing", "DEEP_ARCHIVE", "GLACIER", false, s3store.Pending, tierWaiting},
		// Restore-only: hot object is a no-op; archived waits then done (no transition performed by caller).
		{"restore-only on hot object → skip", "STANDARD", "STANDARD", true, s3store.Readable, tierSkip},
		{"restore-only on glacier, thawing", "GLACIER", "STANDARD", true, s3store.Pending, tierWaiting},
		{"restore-only on glacier, ready → done", "GLACIER", "STANDARD", true, s3store.Readable, tierDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, decideTier(c.current, c.target, c.restoreOnly, c.rd))
		})
	}
}

func TestSameClass(t *testing.T) {
	assert.True(t, sameClass("", "STANDARD"), "empty == STANDARD")
	assert.True(t, sameClass("standard", "STANDARD"), "case-insensitive")
	assert.True(t, sameClass("GLACIER", " glacier "), "trim + case")
	assert.False(t, sameClass("GLACIER", "DEEP_ARCHIVE"))
	assert.False(t, sameClass("GLACIER_IR", "GLACIER"))
}

func TestTierVerb(t *testing.T) {
	assert.Equal(t, "Restoring", tierVerb(&TierRequest{RestoreOnly: true, TargetClass: "STANDARD"}))
	assert.Equal(t, "Archiving", tierVerb(&TierRequest{TargetClass: "GLACIER"}))
	assert.Equal(t, "Archiving", tierVerb(&TierRequest{TargetClass: "DEEP_ARCHIVE"}))
	assert.Equal(t, "Changing storage class", tierVerb(&TierRequest{TargetClass: "STANDARD"}))
}
