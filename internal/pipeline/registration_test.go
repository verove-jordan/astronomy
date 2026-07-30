package pipeline

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seqH renders one registered R-line with the given homography; an all-zero line (FWHM 0) models
// a frame Siril could not register.
func seqH(h [9]float64) string {
	parts := make([]string, 0, 9)
	for _, v := range h {
		parts = append(parts, fmt.Sprintf("%g", v))
	}
	return "R0 2.5 2.6 0.85 0.9 0.01 40 H " + strings.Join(parts, " ")
}

const seqUnregistered = "R0 0 0 0 0 0 0 H 0 0 0 0 0 0 0 0 0"

// writeSeqFixture writes a minimal Siril .seq with the given reference index and R-lines.
func writeSeqFixture(t *testing.T, dir string, ref int, rlines []string) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "S 'light_' 1 %d %d 5 %d 6 0 0 0\nL 1\n", len(rlines), len(rlines), ref)
	for i := range rlines {
		fmt.Fprintf(&b, "I %d 1\n", i+1)
	}
	for _, r := range rlines {
		b.WriteString(r + "\n")
	}
	path := filepath.Join(dir, "light_.seq")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	return path
}

// rotAbout is the rotation-about-a-point homography (deg about cx,cy then translate tx,ty) — the
// shape of a real cross-night registration transform.
func rotAbout(deg, cx, cy, tx, ty float64) [9]float64 {
	rad := deg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	return [9]float64{
		cos, -sin, cx - cos*cx + sin*cy + tx,
		sin, cos, cy - sin*cx - cos*cy + ty,
		0, 0, 1,
	}
}

var idH = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}

func TestReviewMergedRegistration_AnchorsAndMeasures(t *testing.T) {
	// Two groups: a rotated "2020" night (frames 0-2, 140° about centre) and the anchor "2023"
	// night (frames 3-9, dithered identities). The anchor's middle frame must be pinned, the
	// rotated night measured at ~140° with a large overlap, and nothing gated.
	dir := t.TempDir()
	const w, h = 640, 480
	rl := []string{
		seqH(rotAbout(140, w/2, h/2, 4, -2)),
		seqH(rotAbout(140, w/2, h/2, 6, 1)),
		seqH(rotAbout(140, w/2, h/2, -3, 5)),
	}
	for i := 0; i < 7; i++ {
		rl = append(rl, seqH(rotAbout(0, 0, 0, float64(i), float64(-i))))
	}
	path := writeSeqFixture(t, dir, 6, rl)

	spans := []groupSpan{{0, 3}, {3, 10}}
	review := reviewMergedRegistration(path, spans, 1, w, h)

	assert.Equal(t, 7, review.RefIndex, "anchor span middle frame (0-based 6) pinned, 1-based")
	assert.Empty(t, review.Warnings)
	assert.Empty(t, review.PreReject, "a centred 140° rotation is NOT absurd")
	require.Len(t, review.Groups, 2)
	require.NotNil(t, review.Groups[0].RotationDeg)
	assert.InDelta(t, 140, math.Abs(*review.Groups[0].RotationDeg), 1.0, "the 2020 night's rotation is measured")
	assert.Greater(t, *review.Groups[0].OverlapFrac, 0.5)
	require.NotNil(t, review.Groups[1].RotationDeg)
	assert.InDelta(t, 0, *review.Groups[1].RotationDeg, 0.5)
	assert.Equal(t, 3, review.Groups[0].Registered)
	assert.Equal(t, 7, review.Groups[1].Registered)
}

func TestReviewMergedRegistration_GatesAbsurdTransform(t *testing.T) {
	// One frame of the anchor group got a false star match landing it ~6000 px away: it must be
	// excluded from the stack (with the reason) and kept out of the group's medians — under the
	// old framing=min this single frame collapsed the whole channel master (task #312).
	dir := t.TempDir()
	const w, h = 640, 480
	rl := []string{
		seqH(rotAbout(0, 0, 0, 0, 0)),
		seqH(rotAbout(0, 0, 0, 6000, -4000)), // absurd
		seqH(rotAbout(0, 0, 0, 2, 1)),
		seqH(rotAbout(0, 0, 0, -1, 3)),
		seqH(rotAbout(0, 0, 0, 1, -2)),
	}
	path := writeSeqFixture(t, dir, 0, rl)

	review := reviewMergedRegistration(path, []groupSpan{{0, 5}}, 0, w, h)

	assert.Equal(t, 3, review.RefIndex, "middle frame (0-based 2)")
	require.Len(t, review.PreReject, 1)
	assert.Contains(t, review.PreReject[1], "absurd registration transform")
	assert.Equal(t, 1, review.Groups[0].Absurd)
	require.NotNil(t, review.Groups[0].OverlapFrac)
	assert.Greater(t, *review.Groups[0].OverlapFrac, 0.9, "medians exclude the absurd frame")
}

func TestReviewMergedRegistration_MiddleUnregisteredPicksNeighbour(t *testing.T) {
	dir := t.TempDir()
	rl := []string{
		seqH(idH),
		seqH(idH),
		seqUnregistered, // the middle frame Siril dropped
		seqH(idH),
		seqH(idH),
	}
	path := writeSeqFixture(t, dir, 0, rl)

	review := reviewMergedRegistration(path, []groupSpan{{0, 5}}, 0, 640, 480)
	assert.Equal(t, 4, review.RefIndex, "nearest registered neighbour of the middle (0-based 3)")
	assert.Equal(t, 4, review.Groups[0].Registered, "the dropped frame is not re-counted")
}

func TestReviewMergedRegistration_NoAnchorRegisteredFallsBack(t *testing.T) {
	// The whole anchor group failed to register: keep Siril's own reference (no setref), warn,
	// and still measure the other group against it.
	dir := t.TempDir()
	rl := []string{
		seqH(idH),
		seqH(rotAbout(0, 0, 0, 3, 1)),
		seqUnregistered,
		seqUnregistered,
	}
	path := writeSeqFixture(t, dir, 0, rl)

	review := reviewMergedRegistration(path, []groupSpan{{0, 2}, {2, 4}}, 1, 640, 480)
	assert.Zero(t, review.RefIndex, "no setref — Siril's reference stands")
	require.Len(t, review.Warnings, 1)
	assert.Contains(t, review.Warnings[0], "no anchor-night frame registered")
	require.NotNil(t, review.Groups[0].OverlapFrac, "the healthy group is still measured vs Siril's reference")
	assert.Zero(t, review.Groups[1].Registered)
}

func TestReviewMergedRegistration_UnreadableDimsSkipsGate(t *testing.T) {
	dir := t.TempDir()
	path := writeSeqFixture(t, dir, 0, []string{seqH(idH), seqH(idH), seqH(idH)})

	review := reviewMergedRegistration(path, []groupSpan{{0, 3}}, 0, 0, 0)
	assert.Equal(t, 2, review.RefIndex, "the anchor pin still happens")
	assert.Empty(t, review.PreReject, "no gating against a zero canvas")
	require.Len(t, review.Warnings, 1)
	assert.Contains(t, review.Warnings[0], "frame dimensions unreadable")
}

func TestRegistrationLine(t *testing.T) {
	rot, ovl := 141.3, 0.62
	line := registrationLine("L", groupReview{Registered: 28, Absurd: 2, RotationDeg: &rot, OverlapFrac: &ovl})
	assert.Contains(t, line, "L: field rotation +141.3° vs anchor")
	assert.Contains(t, line, "overlap 62%")
	assert.Contains(t, line, "2 absurd transform(s) excluded")

	none := registrationLine("R", groupReview{Absurd: 3})
	assert.Contains(t, none, "no frames survived the transform review (3 absurd)")
}
