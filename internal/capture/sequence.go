package capture

import (
	"fmt"
	"strings"
	"time"
)

// The auto-run sequence: the ordered plan a night is shot to. This is the thing ASICAP makes you
// babysit — pick a filter, set the exposure, count the frames, come back, do it again. Here it is
// declared once and executed.

// Step is one block of the plan: N frames through one filter at one setting.
type Step struct {
	Filter     string `json:"filter"`      // name, matched against the wheel's slot names
	Slot       int    `json:"slot"`        // 1-based wheel slot; 0 → resolve from Filter
	Count      int    `json:"count"`       // frames to take
	ExposureUs int64  `json:"exposure_us"` // 32 µs … 2000 s on an ASI1600
	Gain       int64  `json:"gain"`
	Offset     int64  `json:"offset"`
	Bin        int    `json:"bin"`
	Type       string `json:"type"`      // light|dark|flat|bias|darkflat; empty → light
	DitherN    int    `json:"dither_n"`  // dither every N frames; 0 → never
	DitherPx   int    `json:"dither_px"` // dither box radius in pixels; 0 → 10
}

// Sequence is a whole auto-run: the ordered steps plus how to interleave them.
type Sequence struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
	// Interleave repeats the steps in rotation (L R G B, L R G B, …) instead of shooting each
	// block to completion. Rotating costs filter changes but spreads every channel across the
	// night, so a session cut short by cloud still has usable colour.
	Interleave bool `json:"interleave"`
	// RepeatBlock is how many frames of a step to take before moving on when interleaving; 0 → 1.
	RepeatBlock int `json:"repeat_block"`
}

// Validate rejects a sequence that could not run, before anything is committed to a night.
func (s Sequence) Validate() error {
	if len(s.Steps) == 0 {
		return fmt.Errorf("a sequence needs at least one step")
	}
	for i, step := range s.Steps {
		if step.Count <= 0 {
			return fmt.Errorf("step %d: count must be positive", i+1)
		}
		if step.ExposureUs <= 0 {
			return fmt.Errorf("step %d: exposure must be positive", i+1)
		}
		if strings.TrimSpace(step.Filter) == "" && step.Slot <= 0 && !isCalibration(step.Type) {
			return fmt.Errorf("step %d: a light step needs a filter or a slot", i+1)
		}
		if step.Bin < 0 || step.Bin > 4 {
			return fmt.Errorf("step %d: bin %d is out of range", i+1, step.Bin)
		}
	}
	return nil
}

// TotalFrames is how many exposures the sequence will take.
func (s Sequence) TotalFrames() int {
	n := 0
	for _, step := range s.Steps {
		n += step.Count
	}
	return n
}

// TotalDuration estimates the wall-clock time, exposures only (download and filter changes are
// small and hardware-specific — deliberately not guessed at).
func (s Sequence) TotalDuration() time.Duration {
	var total time.Duration
	for _, step := range s.Steps {
		total += time.Duration(step.ExposureUs) * time.Microsecond * time.Duration(step.Count)
	}
	return total
}

// order flattens the sequence into the exact list of exposures to take, applying the interleave
// policy. Doing it up front means progress, ETA and resume all read from one list rather than
// re-deriving the policy in three places.
func (s Sequence) order() []Step {
	if !s.Interleave {
		out := make([]Step, 0, s.TotalFrames())
		for _, step := range s.Steps {
			for i := 0; i < step.Count; i++ {
				one := step
				one.Count = 1
				out = append(out, one)
			}
		}
		return out
	}
	block := s.RepeatBlock
	if block <= 0 {
		block = 1
	}
	remaining := make([]int, len(s.Steps))
	for i, step := range s.Steps {
		remaining[i] = step.Count
	}
	out := make([]Step, 0, s.TotalFrames())
	for {
		progressed := false
		for i, step := range s.Steps {
			for b := 0; b < block && remaining[i] > 0; b++ {
				one := step
				one.Count = 1
				out = append(out, one)
				remaining[i]--
				progressed = true
			}
		}
		if !progressed {
			return out
		}
	}
}

func isCalibration(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "dark", "bias", "darkflat":
		return true
	}
	return false
}

// Status is a session's lifecycle state.
type Status string

const (
	StatusIdle      Status = "idle"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusAborted   Status = "aborted"
	StatusFailed    Status = "failed"
)

// Progress is the live state of a running session, as the UI renders it.
type Progress struct {
	SessionID     int64     `json:"session_id"`
	Status        Status    `json:"status"`
	StepIndex     int       `json:"step_index"`
	FrameIndex    int       `json:"frame_index"` // frames completed
	TotalFrames   int       `json:"total_frames"`
	CurrentFilter string    `json:"current_filter,omitempty"`
	ExposureUs    int64     `json:"exposure_us,omitempty"`
	ExposureEnds  time.Time `json:"exposure_ends,omitempty"`
	LastPath      string    `json:"last_path,omitempty"`
	Message       string    `json:"message,omitempty"`
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	ETASeconds    float64   `json:"eta_seconds,omitempty"`
	// Captured counts frames actually written, per filter — the number the mosaic plan's progress
	// is reconciled against.
	Captured map[string]int `json:"captured,omitempty"`
}
