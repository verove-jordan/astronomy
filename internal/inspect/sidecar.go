package inspect

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// SharpCap/ASICAP write a per-frame "<name>.FIT.txt" capture-settings sidecar next to each FITS, e.g.
//
//	[ZWO ASI1600MM Pro]
//	EFW Slot = 1(Alias: 1)
//	Exposure = 30s
//	Gain = 300
//	Brightness = 10
//	Temperature = -15.0 C
//
// The "EFW Slot" is the physical filter-wheel position — ground-truth filter identity for mono captures
// that carry no FILTER/IMAGETYP in the header. "Brightness" is ZWO's offset (black-level pedestal), needed
// so a bias/dark matches its lights on gain+offset. We parse only the few keys we use; the rest is ignored.

// sidecarMeta is the subset of a SharpCap sidecar the inspector consumes.
type sidecarMeta struct {
	Slot       int
	SlotAlias  string // canonical filter name from "(Alias: L)" — "" when absent or not a filter token
	Gain       int64
	HasGain    bool
	Offset     int64 // black-level pedestal — SharpCap/ASICAP name it "Brightness" for ZWO cameras
	HasOffset  bool
	ExposureMs int64
	TempMilliC int64
	HasTemp    bool
}

// reSlotAlias captures the wheel-position alias SharpCap appends to the slot number ("1(Alias: L)").
var reSlotAlias = regexp.MustCompile(`\(Alias:\s*([^)]+)\)`)

// readSharpcapSidecar reads the "<fitsPath>.txt" sidecar (so "x.FIT" → "x.FIT.txt", never the unrelated
// "x.Info.txt" star-quality file). ok is false when no sidecar exists or it holds nothing useful — which
// is harmless for captures that don't use SharpCap.
func readSharpcapSidecar(fitsPath string) (sidecarMeta, bool) {
	data, err := os.ReadFile(fitsPath + ".txt")
	if err != nil {
		return sidecarMeta{}, false
	}
	m := parseSharpcapSidecar(string(data))
	return m, m.Slot > 0 || m.HasGain || m.HasOffset || m.ExposureMs > 0 || m.HasTemp
}

// parseSharpcapSidecar parses the "Key = Value" lines of a SharpCap sidecar body.
func parseSharpcapSidecar(text string) sidecarMeta {
	var m sidecarMeta
	for _, raw := range strings.Split(text, "\n") {
		key, val, ok := splitKeyValue(raw)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "efw slot":
			m.Slot = parseLeadingInt(val) // "1(Alias: 1)" → 1
			// The alias is the user's own name for the slot — keep it only when it is a real filter
			// token ("L"/"Ha"/…), canonicalized; numeric or custom aliases ("1") carry no identity.
			if mt := reSlotAlias.FindStringSubmatch(val); mt != nil {
				if f, ok := filterToken(mt[1]); ok {
					m.SlotAlias = f
				}
			}
		case "gain":
			if g, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
				m.Gain, m.HasGain = g, true
			}
		// ZWO's offset (black-level pedestal) is written as "Brightness" by SharpCap/ASICAP; "Offset" is the
		// generic name other capture software uses. Both set the same field. (The unrelated "Auto Exp Target
		// Brightness" key does not match — the switch compares the full key, not a substring.)
		case "brightness", "offset":
			if o, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
				m.Offset, m.HasOffset = o, true
			}
		case "exposure":
			if ms := parseSidecarExposureMs(val); ms > 0 {
				m.ExposureMs = ms
			}
		case "temperature":
			if c, ok := parseSidecarTempC(val); ok {
				m.TempMilliC, m.HasTemp = int64(math.Round(c*1000)), true
			}
		}
	}
	return m
}

// splitKeyValue splits a "key = value" line on its first '='.
func splitKeyValue(line string) (key, val string, ok bool) {
	i := strings.Index(line, "=")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

// parseLeadingInt reads the leading run of digits ("1(Alias: 1)" → 1, "5" → 5); 0 when none.
func parseLeadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// parseSidecarExposureMs reads a SharpCap exposure value ("30s", "1.5s", "30ms", "500us") into ms.
func parseSidecarExposureMs(v string) int64 {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasSuffix(v, "ms"):
		return scaledNumber(strings.TrimSuffix(v, "ms"), 1)
	case strings.HasSuffix(v, "us"):
		return scaledNumber(strings.TrimSuffix(v, "us"), 0.001)
	case strings.HasSuffix(v, "s"):
		return scaledNumber(strings.TrimSuffix(v, "s"), 1000)
	}
	return 0
}

func scaledNumber(s string, scale float64) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * scale))
}

// parseSidecarTempC reads "-15.0 C" / "-15 C" → -15.0.
func parseSidecarTempC(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(v, "C")
	v = strings.TrimSuffix(v, "c")
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f, err == nil
}
