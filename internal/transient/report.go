package transient

import "fmt"

// Summary is the compact, serializable outcome of a channel's trail masking (→ run.json / UI).
type Summary struct {
	Frames     int `json:"frames"`
	WithTrails int `json:"frames_with_trails"`
	Segments   int `json:"segments"`
	MaskedPx   int `json:"masked_px"`
	// Line-validation observability (validated hybrid only): candidates rejected as fixed pattern
	// (walking noise), geostationary swaths repaired, and recurring corridors (belt-reused sky
	// tracks found on the mean residual) repaired. All omitempty so the plain per-pixel summary is
	// unchanged. (line_skipped_frames is gone: the saturated-frame skip was retired when validation
	// became per-candidate and windowed — see maskFrameLines.)
	Rejected      int `json:"rejected_fixed_pattern,omitempty"`
	Geostationary int `json:"geostationary_segments,omitempty"`
	Recurring     int `json:"recurring_segments,omitempty"`
	// SatMaskedPx counts sensor-saturated core pixels repaired from the sub-ceiling median across
	// the sequence (multi-night merges with CoreSatMask; see satmask.go).
	SatMaskedPx int `json:"sat_masked_px,omitempty"`
}

// FrameReport records what MaskSequence changed in one registered frame (1-based Index).
type FrameReport struct {
	Index    int
	Segments int
	MaskedPx int
	// SatPx counts this frame's saturated-core pixels repaired from the sub-ceiling median.
	SatPx int
}

// Report is the full per-frame outcome of MaskSequence.
type Report struct {
	PerFrame []FrameReport
	// PerFrameFallback is true when the sequence had too few frames for a cross-frame statistic and was
	// masked frame-by-frame instead.
	PerFrameFallback bool
	width, height    int // frame dims, for the masked-fraction in Note()
	// Line-validation counters (validated hybrid): candidates rejected as fixed pattern, geostationary
	// swaths repaired, and recurring corridors repaired.
	rejected, geo, recurring int
}

// Summary rolls the per-frame reports into the compact record.
func (r *Report) Summary() Summary {
	s := Summary{Frames: len(r.PerFrame), Rejected: r.rejected, Geostationary: r.geo, Recurring: r.recurring}
	for _, f := range r.PerFrame {
		if f.MaskedPx > 0 {
			s.WithTrails++
		}
		s.Segments += f.Segments
		s.MaskedPx += f.MaskedPx
		s.SatMaskedPx += f.SatPx
	}
	return s
}

// Note returns a human-readable one-liner for the channel notes, or "" when nothing was masked.
func (r *Report) Note() string {
	s := r.Summary()
	if s.MaskedPx == 0 {
		return ""
	}
	if r.width > 0 && r.height > 0 && s.Frames > 0 {
		pct := 100 * float64(s.MaskedPx) / float64(s.Frames*r.width*r.height)
		return fmt.Sprintf("line-aware trail mask: cleaned %d pixels (%.2f%%) across %d frames (%d trail segments in %d frames)",
			s.MaskedPx, pct, s.Frames, s.Segments, s.WithTrails)
	}
	return fmt.Sprintf("line-aware trail mask: cleaned %d pixels (%d trail segments in %d frames)",
		s.MaskedPx, s.Segments, s.WithTrails)
}
