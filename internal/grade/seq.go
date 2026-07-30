// Package grade scores registered sub-frames and decides which to keep. Metrics come from the
// per-frame registration data Siril writes to the .seq file (FWHM, weighted FWHM, roundness,
// quality, background, star count); satellite/plane trails are detected directly on the pixels.
package grade

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
)

// SeqMetric is one frame's registration data parsed from a Siril .seq file. A frame Siril could
// not register has an all-zero R-line (FWHM == 0).
type SeqMetric struct {
	FWHM       float64
	WFWHM      float64
	Roundness  float64
	Quality    float64
	Background float64
	StarCount  int
	// ShiftX/ShiftY are the frame's registration translation (the homography's h02/h12, in pixels,
	// relative to the reference frame) — the capture-time pointing offset that the dither/drift
	// diagnostic classifies. Zero for the reference frame and for unregistered frames.
	ShiftX float64
	ShiftY float64
	// H is the full 3×3 registration homography (row-major, relative to the sequence reference)
	// when the R-line carries one; HasH tells a real matrix apart from an unregistered frame.
	H    [9]float64
	HasH bool
}

// Sequence is the per-image data parsed from a Siril .seq file, in image order.
type Sequence struct {
	Metrics  []SeqMetric // from R<layer> lines
	Included []bool      // from I <n> <incl> lines (Siril's own selection)
	// Reference is the sequence's reference image (0-based, from the S-line; -1 when unset).
	Reference int
}

// ParseSeq reads the I (inclusion) and R<layer> (registration) lines of a Siril .seq file.
// R line format: "R0 fwhm wfwhm roundness quality background nbstars H h00 h01 h02 h10 h11 h12
// h20 h21 h22" (verified on Siril 1.4.3); the homography's h02/h12 are the frame's translation.
func ParseSeq(path string) (*Sequence, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seq := &Sequence{Reference: -1}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "S" && len(fields) >= 7:
			// "S 'name' start nb nb_selected fixed_len reference_image version …"
			seq.Reference = int(atof(fields[6]))
		case fields[0] == "I" && len(fields) >= 3:
			seq.Included = append(seq.Included, fields[2] == "1")
		case len(fields[0]) >= 2 && fields[0][0] == 'R' && fields[0][1] >= '0' && fields[0][1] <= '9' && len(fields) >= 7:
			m := SeqMetric{
				FWHM:       atof(fields[1]),
				WFWHM:      atof(fields[2]),
				Roundness:  atof(fields[3]),
				Quality:    atof(fields[4]),
				Background: atof(fields[5]),
				StarCount:  int(atof(fields[6])),
			}
			if len(fields) >= 17 && fields[7] == "H" {
				for k := 0; k < 9; k++ {
					m.H[k] = atof(fields[8+k])
				}
				m.HasH = true
				m.ShiftX = m.H[2] // h02
				m.ShiftY = m.H[5] // h12
			}
			seq.Metrics = append(seq.Metrics, m)
		}
	}
	return seq, sc.Err()
}

// atof parses a Siril .seq numeric field. Siril writes "nan"/"inf" for metrics it could not
// measure (e.g. a starless or unregistrable frame in a 2-pass registration); those map to 0 — the
// "Siril dropped this frame" sentinel every consumer already branches on. Passing them through
// poisoned run.json + the stored job result, which encoding/json refuses to serialize (task #353),
// and NaN also slips past `<= 0` drop gates.
func atof(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
