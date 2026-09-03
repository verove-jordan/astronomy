package solar

import "testing"

// bestframes_test.go pins the property the export exists for: the frames come back DIFFERENT, not
// merely good. A ranking alone returns the same second twenty times over, because sharpness drifts
// slowly enough that neighbouring frames are near-identical.

func TestSelectSpread_ReturnsDifferentFramesNotTheSameSecond(t *testing.T) {
	// A clip where the sharpest stretch is one short burst: exactly the case a plain ranking fails.
	// Two minutes at 30 fps, with the sharpest stretch confined to a one-second burst: exactly the
	// case a plain ranking fails, and long enough to actually hold the spread being asked for.
	var frames []Frame
	for i := 0; i < 3600; i++ {
		s := 0.2 + 0.05*float64(i%11)/11 // a little variation everywhere, so ties do not decide it
		if i >= 1800 && i < 1830 {
			s = 0.9
		}
		frames = append(frames, Frame{Index: i, TimeMs: int64(i) * 33, Score: s})
	}

	const want = 8
	got := SelectSpread(frames, want, 5000) // at least five seconds apart
	if len(got) != want {
		t.Fatalf("asked for %d frames, got %d", want, len(got))
	}
	for i := 1; i < len(got); i++ {
		if d := got[i].TimeMs - got[i-1].TimeMs; d < 5000 {
			t.Errorf("frames %d and %d are %d ms apart, want at least 5000", i-1, i, d)
		}
		if got[i].TimeMs < got[i-1].TimeMs {
			t.Error("frames came back out of capture order")
		}
	}
	// The burst must still win one slot — the spacing constrains, it does not overrule the ranking.
	burst := 0
	for _, f := range got {
		if f.Score > 0.5 {
			burst++
		}
	}
	if burst != 1 {
		t.Errorf("%d frames came from the sharp burst; want exactly one (the rest spaced away)", burst)
	}

	// And a plain ranking must fail this, or the spacing is guarding nothing.
	plain := SelectSpread(frames, want, 0)
	span := plain[len(plain)-1].TimeMs - plain[0].TimeMs
	t.Logf("spaced selection spans %d ms; unspaced spans %d ms",
		got[len(got)-1].TimeMs-got[0].TimeMs, span)
	if span > 2000 {
		t.Fatalf("fixture proves nothing: an unspaced ranking already spans %d ms", span)
	}
}

func TestSelectSpread_GivesBackWhatItCanRatherThanDuplicates(t *testing.T) {
	// Ten seconds of clip cannot hold twenty frames five seconds apart.
	var frames []Frame
	for i := 0; i < 300; i++ {
		frames = append(frames, Frame{Index: i, TimeMs: int64(i) * 33, Score: float64(i % 7)})
	}
	got := SelectSpread(frames, 20, 5000)
	if len(got) > 3 {
		t.Errorf("returned %d frames from a 10 s clip at a 5 s gap; it should run out, not shrink the gap", len(got))
	}
	if len(got) == 0 {
		t.Error("returned nothing at all")
	}
}
