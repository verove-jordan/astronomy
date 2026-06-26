package skycat

import (
	"strconv"
	"strings"
)

// Normalize upper-cases and strips non-alphanumerics so "M 101", "m-101" and "M101" all collapse to
// the same catalog key. Exported for the target catalog (store) and reuse matching.
func Normalize(s string) string { return normalize(s) }

// ResolveCoords is Resolve returning decimal degrees instead of the "ra,dec" string.
func ResolveCoords(name, dir string) (ra, dec float64, ok bool) {
	coords, found := Resolve(name, dir)
	if !found {
		return 0, 0, false
	}
	return parsePair(coords)
}

// parsePair parses a "ra,dec" decimal-degree string (as produced by Resolve/the catalogs).
func parsePair(coords string) (ra, dec float64, ok bool) {
	parts := strings.Split(coords, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	ra, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	dec, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return ra, dec, true
}

// ParseRA parses a FITS OBJCTRA value to decimal degrees. It accepts sexagesimal hours
// ("13 29 52.7" or "13:29:52.7") and plain decimal degrees ("202.47"). ok is false when empty/invalid.
func ParseRA(s string) (deg float64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	h, m, sec, sexa, valid := parseSexagesimal(s)
	if !valid {
		return 0, false
	}
	if !sexa { // already decimal degrees
		return h, true
	}
	return (h + m/60 + sec/3600) * 15, true // hours → degrees
}

// ParseDec parses a FITS OBJCTDEC value to decimal degrees. It accepts sexagesimal degrees
// ("+47 11 43" or "-05:23:00") and plain decimal degrees ("47.19"). ok is false when empty/invalid.
func ParseDec(s string) (deg float64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	d, m, sec, sexa, valid := parseSexagesimal(s)
	if !valid {
		return 0, false
	}
	if !sexa {
		return d, true
	}
	sign := 1.0
	if strings.HasPrefix(s, "-") {
		sign = -1
	}
	abs := absVal(d) + m/60 + sec/3600
	return sign * abs, true
}

// parseSexagesimal splits a space/colon-separated triple into its components. When the value is a
// single number it is returned in a (decimal, false) form. valid is false for unparseable input.
func parseSexagesimal(s string) (a, b, c float64, sexagesimal, valid bool) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ':' })
	switch len(fields) {
	case 1:
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, 0, 0, false, false
		}
		return v, 0, 0, false, true
	case 2, 3:
		nums := make([]float64, 3)
		for i := 0; i < len(fields); i++ {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return 0, 0, 0, false, false
			}
			nums[i] = v
		}
		return nums[0], nums[1], nums[2], true, true
	default:
		return 0, 0, 0, false, false
	}
}

func absVal(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
