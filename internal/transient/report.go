package transient

import "fmt"

// Summary is the compact, serializable outcome of a channel's trail masking (→ run.json / UI).
type Summary struct {
	Frames     int `json:"frames"`
	WithTrails int `json:"frames_with_trails"`
	Segments   int `json:"segments"`
	MaskedPx   int `json:"masked_px"`
}

// FrameReport records what MaskSequence changed in one registered frame (1-based Index).
type FrameReport struct {
	Index    int
	Segments int
	MaskedPx int
}

// Report is the full per-frame outcome of MaskSequence.
type Report struct {
	PerFrame []FrameReport
	// PerFrameFallback is true when the sequence had too few frames for a cross-frame statistic and was
	// masked frame-by-frame instead.
	PerFrameFallback bool
	width, height    int // frame dims, for the masked-fraction in Note()
}

// Summary rolls the per-frame reports into the compact record.
func (r *Report) Summary() Summary {
	s := Summary{Frames: len(r.PerFrame)}
	for _, f := range r.PerFrame {
		if f.MaskedPx > 0 {
			s.WithTrails++
		}
		s.Segments += f.Segments
		s.MaskedPx += f.MaskedPx
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
