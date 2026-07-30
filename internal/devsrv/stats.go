package devsrv

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Live-frame statistics: the numbers ASICAP shows next to the image, computed here rather than in
// the browser so the client only ever receives a few hundred bytes per frame instead of 32 MB.

// histogramBins is the resolution of the live histogram. 512 is enough to see the sky peak and the
// clipping shoulder without making the payload big.
const histogramBins = 512

// FrameStats is one frame's summary.
type FrameStats struct {
	Min          uint16   `json:"min"`
	Max          uint16   `json:"max"`
	Mean         float64  `json:"mean"`
	Median       uint16   `json:"median"`
	StdDev       float64  `json:"std_dev"`
	SaturatedPct float64  `json:"saturated_pct"`
	Histogram    []uint32 `json:"histogram"`
	Bins         int      `json:"bins"`
	// AutoLo/AutoHi are the display stretch the viewer starts from — a robust percentile pair, the
	// same idea as the file preview's auto-stretch.
	AutoLo uint16 `json:"auto_lo"`
	AutoHi uint16 `json:"auto_hi"`

	Width      int   `json:"width"`
	Height     int   `json:"height"`
	ExposureUs int64 `json:"exposure_us"`
	Gain       int64 `json:"gain"`
	TempMilliC int   `json:"temp_milli_c"`
	HasTemp    bool  `json:"has_temp"`

	// Focus is filled in by the focus meter (Phase 4) when it has a reliable reading.
	Focus *FocusStats `json:"focus,omitempty"`
}

// FocusStats is the focus meter's verdict for one frame.
type FocusStats struct {
	Score       float64   `json:"score"` // 0–100, 100 = as sharp as this session has managed
	HFDPx       float64   `json:"hfd_px"`
	HFDArcsec   float64   `json:"hfd_arcsec,omitempty"`
	Stars       int       `json:"stars"`
	Saturated   bool      `json:"saturated"`
	Reliable    bool      `json:"reliable"`
	DistanceUm  float64   `json:"distance_um,omitempty"`  // how far the focuser is from focus
	Turns       float64   `json:"turns,omitempty"`        // that distance in focuser turns
	Advice      string    `json:"advice,omitempty"`       // "better"|"worse"|"first"|"at_focus"
	BestHFDPx   float64   `json:"best_hfd_px,omitempty"`  // best seen this session
	TiltCorners []float64 `json:"tilt_corners,omitempty"` // per-corner HFD, for tilt
}

// sampleStride keeps the statistics cheap on a 16-megapixel frame: every Nth pixel is enough for a
// histogram and robust percentiles, and it turns a 30 ms pass into a 2 ms one.
const sampleStride = 4

// measureFrame computes the live statistics of a frame.
func measureFrame(f *device.Frame) *FrameStats {
	st := &FrameStats{
		Bins: histogramBins, Histogram: make([]uint32, histogramBins),
		Width: f.Width, Height: f.Height,
		ExposureUs: f.ExposureUs, Gain: f.Gain,
		TempMilliC: f.TempMilliC, HasTemp: f.HasTemp,
		Min: 65535,
	}
	if len(f.Pix) == 0 {
		st.Min = 0
		return st
	}
	var sum, sumSq float64
	n := 0
	saturated := 0
	sample := make([]uint16, 0, len(f.Pix)/sampleStride+1)
	for i := 0; i < len(f.Pix); i += sampleStride {
		v := f.Pix[i]
		if v < st.Min {
			st.Min = v
		}
		if v > st.Max {
			st.Max = v
		}
		if v >= 65000 {
			saturated++
		}
		fv := float64(v)
		sum += fv
		sumSq += fv * fv
		st.Histogram[int(v)*histogramBins/65536]++
		sample = append(sample, v)
		n++
	}
	if n == 0 {
		return st
	}
	st.Mean = sum / float64(n)
	variance := sumSq/float64(n) - st.Mean*st.Mean
	if variance > 0 {
		st.StdDev = math.Sqrt(variance)
	}
	st.SaturatedPct = 100 * float64(saturated) / float64(n)

	sort.Slice(sample, func(i, j int) bool { return sample[i] < sample[j] })
	st.Median = percentile(sample, 0.5)
	st.AutoLo, st.AutoHi = autoStretch(sample)
	return st
}

// autoStretch picks display black/white points: just below the sky peak and well above it, so stars
// show without the background washing out. Percentiles rather than min/max, because one hot pixel
// would otherwise set the white point.
func autoStretch(sorted []uint16) (lo, hi uint16) {
	if len(sorted) == 0 {
		return 0, 65535
	}
	lo = percentile(sorted, 0.02)
	hi = percentile(sorted, 0.999)
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

func percentile(sorted []uint16, p float64) uint16 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
