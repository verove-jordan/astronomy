package eclipsegeom

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionSpans is what the 12 Aug 2026 recording actually covers: six clips, read from their own
// container creation times and durations.
func sessionSpans() []Span {
	clip := func(h, mi, sec int, dur time.Duration) Span {
		from := time.Date(2026, 8, 12, h, mi, sec, 0, time.UTC)
		return Span{From: from, To: from.Add(dur)}
	}
	return []Span{
		clip(17, 30, 6, 15980*time.Millisecond),
		clip(17, 47, 51, 97985*time.Millisecond),
		clip(18, 11, 49, 1049450*time.Millisecond),
		clip(18, 39, 54, 300190*time.Millisecond),
		clip(18, 46, 40, 446575*time.Millisecond),
		clip(19, 12, 33, 85668*time.Millisecond),
	}
}

func TestPlanLadder_TheSession(t *testing.T) {
	panels, notes, err := PlanLadder(sessionSpans(), piriac, 11)
	require.NoError(t, err)

	for _, p := range panels {
		t.Logf("%-8s %s  mag %.3f  obsc %5.1f%%  target %.3f  miss %.3f",
			p.Side, p.At.Format("15:04:05"), p.Magnitude, p.Obscuration*100, p.TargetMag, p.MissMag)
	}
	for _, n := range notes {
		t.Logf("note: %s", n)
	}

	require.NotEmpty(t, panels)
	assert.Equal(t, 1, countSide(panels, Peak), "exactly one maximum, in the middle")
	assert.Equal(t, countSide(panels, Ingress), countSide(panels, Egress), "the two halves match in count")
	assert.Equal(t, Peak, panels[len(panels)/2].Side, "maximum sits at the centre of the sequence")

	// Every panel is a real instant inside a real clip: nothing is mirrored or interpolated.
	for _, p := range panels {
		assert.True(t, covered(p.At), "%s panel at %s falls inside a clip", p.Side, p.At.Format("15:04:05"))
	}
	// Ingress runs shallow to deep, egress deep to shallow.
	assertMonotone(t, panels)
}

func TestPlanLadder_PairsAreCloseInPhase(t *testing.T) {
	panels, _, err := PlanLadder(sessionSpans(), piriac, 11)
	require.NoError(t, err)

	var worst, total float64
	var rungs int
	for _, p := range panels {
		if p.Side != Ingress {
			continue
		}
		rungs++
		total += p.MissMag
		if p.MissMag > worst {
			worst = p.MissMag
		}
	}
	require.Positive(t, rungs)
	mean := total / float64(rungs)
	t.Logf("%d rungs, mean miss %.3f magnitude, worst %.3f", rungs, mean, worst)

	assert.LessOrEqual(t, worst, magMatchTol, "no rung may exceed the matching tolerance")
	assert.Less(t, mean, 0.06, "on average the two sides of a rung show the same phase")
}

func TestPlanLadder_RefusesWhatItCannotMatch(t *testing.T) {
	// Only the ingress half was recorded: no rung can be paired, so no sequence is possible.
	ingressOnly := []Span{{
		From: time.Date(2026, 8, 12, 17, 30, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC),
	}}
	_, _, err := PlanLadder(ingressOnly, piriac, 9)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "egress")
}

func TestPlanLadder_SaysSoWhenItPlacesFewerPanelsThanAsked(t *testing.T) {
	_, notes, err := PlanLadder(sessionSpans(), piriac, 21)
	require.NoError(t, err)

	require.NotEmpty(t, notes)
	assert.Contains(t, notes[len(notes)-1], "not the 21 asked for")
}

func TestPlanLadder_NoEclipseHereIsAnError(t *testing.T) {
	// Same clock, wrong hemisphere: the Moon never touches the Sun as seen from Sydney.
	sydney := Site{LatDeg: -33.8688, LonDeg: 151.2093}
	_, _, err := PlanLadder(sessionSpans(), sydney, 9)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no eclipse")
}

func countSide(panels []Panel, s Side) int {
	n := 0
	for _, p := range panels {
		if p.Side == s {
			n++
		}
	}
	return n
}

func covered(at time.Time) bool {
	for _, sp := range sessionSpans() {
		if !at.Before(sp.From) && !at.After(sp.To) {
			return true
		}
	}
	return false
}

func assertMonotone(t *testing.T, panels []Panel) {
	t.Helper()
	prev := -1.0
	for _, p := range panels {
		if p.Side == Egress {
			break
		}
		assert.Greater(t, p.Magnitude, prev, "the run into maximum deepens")
		prev = p.Magnitude
	}
	prev = 2.0
	for _, p := range panels {
		if p.Side == Ingress {
			continue
		}
		assert.Less(t, p.Magnitude, prev, "the run out of maximum thins")
		prev = p.Magnitude
	}
}
