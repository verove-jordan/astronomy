package solar

import (
	"os"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

// fileMeta is the metadata half of a probe, unified across stills and clips.
type fileMeta struct {
	Width, Height   int
	ISO             int64
	ExposureMs      int64
	FocalLength35mm int
	CameraModel     string
	TakenAtMs       int64
}

// readStillMeta reads a still's camera metadata, falling back to the file's modification time when
// no capture timestamp is recoverable — session windowing needs *some* ordering, and for a folder
// copied straight off a phone the mtime preserves capture order even when EXIF does not.
func readStillMeta(path string) fileMeta {
	m := rawmeta.Read(path)
	fm := fileMeta{
		Width: m.Width, Height: m.Height,
		ISO: m.ISO, ExposureMs: m.ExposureMs,
		FocalLength35mm: m.FocalLength35mm,
		CameraModel:     m.CameraModel,
		TakenAtMs:       m.TakenAtMs,
	}
	if fm.TakenAtMs == 0 {
		if st, err := os.Stat(path); err == nil {
			fm.TakenAtMs = st.ModTime().UnixMilli()
		}
	}
	return fm
}

// medianOf takes the median of one field across a set of probes.
func medianOf(probes []FrameProbe, get func(FrameProbe) float64) float64 {
	if len(probes) == 0 {
		return 0
	}
	v := make([]float64, len(probes))
	for i, p := range probes {
		v[i] = get(p)
	}
	sort.Float64s(v)
	mid := len(v) / 2
	if len(v)%2 == 1 {
		return v[mid]
	}
	return (v[mid-1] + v[mid]) / 2
}

// median returns the median of a float slice (it sorts a copy).
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// tailLines returns the last n lines of s, for attaching a tool's real complaint to an error.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
