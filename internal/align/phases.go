package align

// Two-phase alignment routines (Celestron EQ): a small set of alignment stars builds the initial
// pointing model, then calibration stars — by brand rule on the OPPOSITE side of the meridian —
// refine it (cone error). Single-phase profiles (AlignStars == 0) are untouched by this file.

// phaseSplit divides the requested total star count into the alignment and calibration phases.
func phaseSplit(profile Profile, count int) (alignN, calibN int) {
	if profile.AlignStars <= 0 || count <= profile.AlignStars {
		return count, 0
	}
	return profile.AlignStars, count - profile.AlignStars
}

// oppositeSide flips a meridian side; "any" stays "any".
func oppositeSide(side string) string {
	switch side {
	case "east":
		return "west"
	case "west":
		return "east"
	}
	return side
}

// fillCalibration extends chosen with `need` calibration stars. With CalibOppositeSide the pool is
// the opposite meridian side from the alignment stars; when that side can't supply everything the
// remainder falls back to the alignment side with a warning — the hand controller accepts a
// calibration star anywhere in the sky, opposite-side is the recommendation, and returning fewer
// stars would strand the user mid-procedure. greedyFill measures spread against every star already
// chosen, so calibration picks stay wide of the alignment pair too.
func fillCalibration(chosen, cands []positioned, profile Profile, alignSide string, rejected map[string]bool, accepted []positioned, need int) ([]positioned, []Warning) {
	if need <= 0 {
		return chosen, nil
	}
	side := alignSide
	if profile.CalibOppositeSide {
		side = oppositeSide(alignSide)
	}
	pool := eligible(cands, profile, side, rejected, accepted)
	out, added := greedyFill(chosen, pool, profile, need)
	if added >= need || !profile.CalibOppositeSide || side == "any" {
		return out, nil
	}

	// Opposite side dry — place the remainder on the alignment side and say so.
	fallbackPool := eligible(cands, profile, alignSide, rejected, accepted)
	out, more := greedyFill(out, fallbackPool, profile, need-added)
	if more == 0 {
		return out, nil // total shortage: planWarnings already reports it
	}
	return out, []Warning{{Code: "calib_same_side", Side: side, Count: more}}
}
