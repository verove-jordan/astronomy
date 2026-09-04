package nexstar

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// movedTheMount reports whether anything in the conversation could have turned the telescope or
// changed a setting. The audit's whole claim is that it is safe to run in the middle of a session,
// and the only way to hold that claim is to assert it against the frames actually sent.
func movedTheMount(hc *fakeHC) (string, bool) {
	for _, c := range hc.commands() {
		if len(c) == 0 {
			continue
		}
		switch c[0] {
		case 'r', 'R', 's', 'S', 'M', 'T', 'W', 'H', 'x', 'y':
			return c[:1], true
		case 'P':
			if len(c) < 8 {
				continue
			}
			switch c[3] {
			case 6, 7, 36, 37:
				// A rate command is only harmless when the rate is zero.
				if c[4] != 0 || c[5] != 0 {
					return "rate slew", true
				}
			case mcSeekIndex, mcPECWriteData, mcPECRecordStop, mcPECPlayback,
				mcSetAutoguideRate, mcSetPosition, mcGotoFast, mcGotoSlow:
				return "motor write", true
			}
		}
	}
	return "", false
}

func TestAudit_ReadsWhatIsStoredAndMovesNothing(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	r, err := Audit(context.Background(), m)
	require.NoError(t, err)

	assert.Equal(t, "Advanced VX", r.Identity.Model)
	assert.Equal(t, 20, r.Identity.ModelCode)
	assert.Equal(t, "5.30", r.Identity.Firmware, "the HAND CONTROLLER's version, which the 4.15 reset rule turns on")
	assert.Equal(t, "7.11", r.Identity.RAMotorFirmware, "the motor boards have their own firmware")
	assert.Equal(t, "7.11", r.Identity.DecMotorFirmware)

	require.True(t, r.Site.Read)
	assert.InDelta(t, 48.8566, r.Site.LatDeg, 1.0/3600, "whole arcseconds is all the protocol carries")
	assert.InDelta(t, 2.3522, r.Site.LonDeg, 1.0/3600)

	require.True(t, r.Clock.Read)
	assert.InDelta(t, 0, r.Clock.SkewSec, 5, "a fake set from this machine's clock reads back as this machine's clock")

	require.True(t, r.Drive.Read)
	assert.True(t, r.Drive.Tracking)
	assert.Equal(t, "sidereal", r.Drive.TrackingRate)
	assert.True(t, r.Drive.Aligned)

	require.True(t, r.Guide.Read)
	assert.True(t, r.Guide.BothAxes, "a hand controller can be asked about both motors")
	assert.Equal(t, 128, r.Guide.RAUnits)
	assert.Equal(t, 128, r.Guide.DecUnits)
	assert.False(t, r.Guide.Mismatch)

	require.True(t, r.PEC.Supported)
	require.True(t, r.PEC.Read)
	assert.Equal(t, 88, r.PEC.Bins)
	assert.Len(t, r.PEC.Curve, 88)
	assert.True(t, r.PEC.AllZero, "a mount that has never been trained holds a table of zeros")

	what, moved := movedTheMount(hc)
	assert.False(t, moved, "the audit must send nothing that changes state, but sent %q", what)
}

// The point of reading the table is to say what it would DO, in the units the mount's error is
// measured in. A list of eighty-eight signed bytes answers nothing on its own.
func TestAudit_TranslatesTheTableIntoArcseconds(t *testing.T) {
	hc := newFakeHC()
	// A balanced correction: half the revolution pushed one way, half the other. This is the shape a
	// real worm correction has, and it must come back to where it started.
	for i := range hc.pecTable {
		if i < len(hc.pecTable)/2 {
			hc.pecTable[i] = 10
		} else {
			hc.pecTable[i] = -10
		}
	}
	m := testMount(t, hc)

	r, err := Audit(context.Background(), m)
	require.NoError(t, err)

	assert.False(t, r.PEC.AllZero)
	assert.Equal(t, 10, r.PEC.PeakUnits)
	assert.InDelta(t, 10*r.PEC.LSBArcsecPerSec, r.PEC.PeakRateArcsecPerSec, 1e-9)
	// 44 bins at 10 units, each bin 10 × LSB × binSec of movement.
	assert.InDelta(t, 44*10*r.PEC.LSBArcsecPerSec*r.PEC.BinSec, r.PEC.SwingArcsec, 1e-6)
	assert.InDelta(t, 0, r.PEC.NetArcsecPerRev, 1e-6, "a correction must return to where it started")
}

// The failure worth naming: a table whose bins do not sum to zero is a constant rate error bolted
// onto sidereal. It does not look like periodic error at all — it looks like a mount that trails
// steadily in one direction all night, on a night when the polar alignment is good.
func TestAudit_NamesATableThatDoesNotAverageToZero(t *testing.T) {
	hc := newFakeHC()
	for i := range hc.pecTable {
		hc.pecTable[i] = 10 // every bin pushing the same way
	}
	m := testMount(t, hc)

	r, err := Audit(context.Background(), m)
	require.NoError(t, err)

	assert.Greater(t, r.PEC.NetArcsecPerRev, 1.0)
	assert.Contains(t, notesJoined(r), "does not average to zero")
}

// Every hand controller sets the two motors together, so a pair that disagree was set by software —
// and it is one of the very few durable settings that can make one axis behave unlike the other.
func TestAudit_NoticesMotorsSetToDifferentGuideRates(t *testing.T) {
	hc := newFakeHC()
	hc.guideRate, hc.guideRateDec = 128, 32
	m := testMount(t, hc)

	r, err := Audit(context.Background(), m)
	require.NoError(t, err)

	assert.True(t, r.Guide.Mismatch)
	assert.Equal(t, 128, r.Guide.RAUnits)
	assert.Equal(t, 32, r.Guide.DecUnits)
	assert.Contains(t, notesJoined(r), "DIFFERENT autoguide rates")
}

// Playback is the one thing in the report that is NOT a reading, and saying so is the difference
// between an audit and a guess.
func TestAudit_SaysPlaybackCannotBeRead(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	r, err := Audit(context.Background(), m)
	require.NoError(t, err)
	assert.Contains(t, notesJoined(r), "cannot be read back over the link")
	assert.Contains(t, notesJoined(r), "Anti-backlash is not shown")
}

func TestAudit_RequiresAConnection(t *testing.T) {
	m := New("/dev/nothing", nil)
	_, err := Audit(context.Background(), m)
	assert.Error(t, err)
}

// --- restore ------------------------------------------------------------------------------------

// The table already in the mount may be the only copy of an hour somebody spent with a hand
// controller. Without somewhere to put it, this must not run at all.
func TestRestore_RefusesWithoutSomewhereToPutTheBackup(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	_, err := Restore(context.Background(), m, RestoreOptions{PEC: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup")

	what, moved := movedTheMount(hc)
	assert.False(t, moved, "a refused restore writes nothing, but sent %q", what)
}

func TestRestore_DryRunBacksUpAndSendsNothing(t *testing.T) {
	hc := newFakeHC()
	hc.pecTable[3] = 42
	m := testMount(t, hc)
	dir := t.TempDir()

	res, err := Restore(context.Background(), m, RestoreOptions{
		PEC: true, PECPlayback: true, GuideRate: true, BackupDir: dir, DryRun: true,
	})
	require.NoError(t, err)

	require.NotEmpty(t, res.BackupPath)
	saved, err := os.ReadFile(res.BackupPath)
	require.NoError(t, err)
	var restored AuditReport
	require.NoError(t, json.Unmarshal(saved, &restored))
	assert.Equal(t, 42, restored.PEC.Curve[3], "the raw bins are in the backup, not just a summary")

	assert.Len(t, res.Actions, 3)
	for _, a := range res.Actions {
		assert.False(t, a.Applied, "a dry run applies nothing")
	}
	what, moved := movedTheMount(hc)
	assert.False(t, moved, "a dry run sends nothing, but sent %q", what)
	assert.Equal(t, int8(42), hc.pecTable[3], "and the mount still holds what it held")
}

// Writing zeros IS the erase — the hand controller has no menu item for it — and unlike the menu
// this can be proven afterwards.
func TestRestore_ZeroesTheTableAndProvesIt(t *testing.T) {
	hc := newFakeHC()
	for i := range hc.pecTable {
		hc.pecTable[i] = int8(i - 44)
	}
	hc.pecPlaying = true
	m := testMount(t, hc)

	res, err := Restore(context.Background(), m, RestoreOptions{
		PEC: true, PECPlayback: true, BackupDir: t.TempDir(),
	})
	require.NoError(t, err)

	for _, a := range res.Actions {
		assert.True(t, a.Applied, "%s: %s", a.Item, a.Err)
	}
	assert.False(t, hc.pecPlaying, "playback stops before the table is rewritten")
	for i, v := range hc.pecTable {
		require.Zero(t, v, "bin %d", i)
	}
	require.True(t, res.After.PEC.Read)
	assert.True(t, res.After.PEC.AllZero, "the after-reading is what proves it, not the write returning nil")
}

// Playback must stop BEFORE the table is rewritten: the other order replays a half-written table for
// the couple of seconds the write takes.
func TestRestore_StopsPlaybackBeforeRewriting(t *testing.T) {
	hc := newFakeHC()
	hc.pecTable[0] = 7
	m := testMount(t, hc)

	_, err := Restore(context.Background(), m, RestoreOptions{
		PEC: true, PECPlayback: true, BackupDir: t.TempDir(),
	})
	require.NoError(t, err)

	firstWrite, playbackOff := -1, -1
	for i, c := range hc.commands() {
		if len(c) < 8 || c[0] != 'P' {
			continue
		}
		if c[3] == mcPECWriteData && firstWrite < 0 {
			firstWrite = i
		}
		if c[3] == mcPECPlayback && c[4] == 0 && playbackOff < 0 {
			playbackOff = i
		}
	}
	require.Positive(t, firstWrite)
	require.Positive(t, playbackOff)
	assert.Less(t, playbackOff, firstWrite)
}

func TestRestore_SetsBothMotorsToTheNamedDefault(t *testing.T) {
	hc := newFakeHC()
	hc.guideRate, hc.guideRateDec = 200, 12
	m := testMount(t, hc)

	res, err := Restore(context.Background(), m, RestoreOptions{GuideRate: true, BackupDir: t.TempDir()})
	require.NoError(t, err)

	assert.Equal(t, byte(128), hc.guideRate, "half sidereal, from the named constant rather than a number invented here")
	assert.Equal(t, byte(128), hc.guideRateDec, "both motors, because that is what makes them agree again")
	assert.False(t, res.After.Guide.Mismatch)
}

func TestRestore_RewritesSiteClockAndTracking(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	when := time.Now()

	res, err := Restore(context.Background(), m, RestoreOptions{
		Site: true, SiteLatDeg: 43.6047, SiteLonDeg: 1.4442,
		Clock: true, ClockTime: when,
		Tracking: true, TrackingOn: true, TrackingRate: "sidereal",
		BackupDir: t.TempDir(),
	})
	require.NoError(t, err)
	for _, a := range res.Actions {
		require.True(t, a.Applied, "%s: %s", a.Item, a.Err)
	}

	assert.InDelta(t, 43.6047, res.After.Site.LatDeg, 1.0/3600)
	assert.InDelta(t, 1.4442, res.After.Site.LonDeg, 1.0/3600)
	assert.InDelta(t, 0, res.After.Clock.UTC.Sub(when).Seconds(), 3)
	assert.True(t, res.After.Drive.Tracking)
}

// A restore that was asked for nothing must say so rather than reporting a success it did not have.
func TestRestore_NothingSelectedIsSaidOutLoud(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	res, err := Restore(context.Background(), m, RestoreOptions{BackupDir: t.TempDir()})
	require.NoError(t, err)
	assert.Empty(t, res.Actions)
	assert.Contains(t, res.String(), "nothing selected")
}

func TestAuditReport_StringAndJSONAreUsable(t *testing.T) {
	hc := newFakeHC()
	hc.pecTable[5] = 20
	m := testMount(t, hc)

	r, err := Audit(context.Background(), m)
	require.NoError(t, err)

	out := r.String()
	assert.Contains(t, out, "Advanced VX")
	assert.Contains(t, out, "hand controller")
	assert.Contains(t, out, "motor controllers")
	assert.Contains(t, out, "PEC")

	b, err := r.JSON()
	require.NoError(t, err)
	assert.Contains(t, string(b), "\"curve\"")
}

// Every label must be separated from its value. "  PEC playback" is exactly as wide as the column
// used to be, so it rendered as "PEC playbacklast commanded by this driver: false" — in a report
// whose whole purpose is being read by a person at 3am. The two longest labels are checked, because
// the collision is a function of label length and they are the ones that can reach the column.
func TestAuditReport_StringSeparatesTheLongestLabelsFromTheirValues(t *testing.T) {
	m := testMount(t, newFakeHC())
	r, err := Audit(context.Background(), m)
	require.NoError(t, err)

	out := r.String()
	assert.Regexp(t, `PEC playback\s+last commanded`, out)
	assert.Regexp(t, `guide rate\s+RA `, out)
}

// A preview followed by an apply lands inside the same second. Replacing the first backup with the
// second would throw away the older state, which is always the one worth keeping.
func TestWriteBackup_NeverOverwritesAnEarlierOne(t *testing.T) {
	dir := t.TempDir()

	first, err := writeBackup(AuditReport{AtMs: 1}, dir)
	require.NoError(t, err)
	second, err := writeBackup(AuditReport{AtMs: 2}, dir)
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.FileExists(t, first)
	assert.FileExists(t, second)
}

func TestWriteBackup_CreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "output")
	path, err := writeBackup(AuditReport{AtMs: 1}, dir)
	require.NoError(t, err)
	assert.FileExists(t, path)
}

func notesJoined(r AuditReport) string {
	out := ""
	for _, n := range r.Notes {
		out += n + "\n"
	}
	return out
}
