package panelgroup

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/pointing"
)

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi
)

// centerOf returns the panel's mean pointing. The axis is the normalized mean of the frames' unit
// vectors rather than a mean of azimuths, which would be meaningless near the zenith and wrong
// across due north; roll is averaged circularly for the same reason.
//
// The centre carries the panel's MID capture time, not its first. The tripod holds one aim for the
// whole panel, but the sky under it turns, so the middle is the instant at which the mean pointing
// and the mean sky agree.
func centerOf(frames []Frame) pointing.Frame {
	var sum [3]float64
	var sinRoll, cosRoll float64
	for _, f := range frames {
		axis := f.Pointing.Axis()
		for i := range sum {
			sum[i] += axis[i]
		}
		sinRoll += math.Sin(f.Pointing.RollDeg * deg2rad)
		cosRoll += math.Cos(f.Pointing.RollDeg * deg2rad)
	}

	first := frames[0].Pointing
	center := pointing.Frame{
		LatDeg:  first.LatDeg,
		LonDeg:  first.LonDeg,
		HasSite: first.HasSite,
		HasTime: first.HasTime,
		RollDeg: math.Atan2(sinRoll, cosRoll) * rad2deg,
	}
	if center.HasTime {
		span := frames[len(frames)-1].At.Sub(frames[0].At)
		center.At = frames[0].At.Add(span / 2)
	}

	n := math.Sqrt(sum[0]*sum[0] + sum[1]*sum[1] + sum[2]*sum[2])
	if n == 0 {
		return center // frames cancelled out exactly; leave the axis at the zero value
	}
	center.AltDeg = math.Asin(clamp1(sum[2]/n)) * rad2deg
	center.AzDeg = norm360(math.Atan2(sum[0], sum[1]) * rad2deg)
	return center
}

// spreadOf is the largest angle any frame sits from the centre.
func spreadOf(frames []Frame, center pointing.Frame) float64 {
	worst := 0.0
	for _, f := range frames {
		if sep := pointing.SeparationDeg(center, f.Pointing); sep > worst {
			worst = sep
		}
	}
	return worst
}

// angleDiff is the signed difference between two angles in degrees, wrapped to (-180, 180], so a
// roll crossing zero reads as a small change rather than a full turn.
func angleDiff(a, b float64) float64 {
	d := math.Mod(a-b+540, 360) - 180
	return d
}

func clamp1(v float64) float64 { return math.Max(-1, math.Min(1, v)) }

func norm360(d float64) float64 { return math.Mod(math.Mod(d, 360)+360, 360) }
