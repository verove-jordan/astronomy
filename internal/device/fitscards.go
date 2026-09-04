package device

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// The capture metadata contract. Everything downstream — calibration matching, night sessionization,
// photometric normalisation, mosaic panel segmentation — is driven by these header cards, and every
// mistake here degrades SILENTLY (a frame lands in the wrong set, or in no set at all) rather than
// failing loudly. The rules below are not stylistic; each one is what internal/inspect actually
// parses:
//
//   - DATE-OBS must be timezone-NAIVE ("2026-07-27T22:14:03.250"). inspect/classify.go has no layout
//     with a "Z" or an offset, so a suffix makes DateObsMs zero → no night key → sessionization,
//     per-night flats and cross-session reuse all quietly stop working.
//   - The software tag is SWCREATE, not CREATOR. It is the evidence that gates the ZWO gain law in
//     photometric normalisation.
//   - IMAGETYP values are substring-matched: "Light Frame"/"Dark Frame"/"Flat Frame"/"Bias Frame".
//   - EXPTIME is in SECONDS (EXPOINUS, in microseconds, is the legacy ASICAP fallback).
//   - CCD-TEMP is read; SET-TEMP is not — but writing both costs nothing and documents the run.
//
// See also FileName below: inspect vetoes files whose name contains processed-output tokens.

// FrameMeta is everything known about one captured frame at write time.
type FrameMeta struct {
	Type          string // "light" | "dark" | "flat" | "bias" | "darkflat"
	Filter        string
	ExposureUs    int64
	Gain          int64
	Offset        int64
	Bin           int
	TempMilliC    int
	HasTemp       bool
	TargetTempC   float64
	HasTargetTemp bool

	Object   string
	Instrume string // camera model, e.g. "ZWO ASI1600MM Pro"
	Telescop string

	FocalLenMM  float64
	PixelSizeUm float64

	// Pointing, when the mount knows it. Written as sexagesimal OBJCTRA/OBJCTDEC, which is what
	// inspect + the plate-solve hint ladder read.
	RADeg    float64
	DecDeg   float64
	HasCoord bool

	// Panel is the mosaic tile folder ("p03") when this frame belongs to a tiled mosaic. Recorded
	// so a frame can be traced back to its tile even if the folder layout is later rearranged.
	Panel string

	StartedAt time.Time // exposure start, in LOCAL time (see DATE-OBS rule above)
	SessionID string    // capture-session identifier, for provenance
}

// imageTypeCard maps our internal type onto a value inspect's classifier recognises.
func imageTypeCard(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "dark":
		return "Dark Frame"
	case "flat":
		return "Flat Frame"
	case "bias", "offset", "zero":
		return "Bias Frame"
	case "darkflat", "dark_flat", "flatdark":
		return "Dark Flat Frame"
	default:
		return "Light Frame"
	}
}

// Cards renders the FITS header cards for a captured frame, pre-padded to 80 columns and ready to
// hand to fits.Write16.
func (m FrameMeta) Cards() []string {
	cards := []string{
		strCard("IMAGETYP", imageTypeCard(m.Type)),
		numCard("EXPTIME", trimFloat(float64(m.ExposureUs)/1e6, 6)),
		numCard("EXPOSURE", trimFloat(float64(m.ExposureUs)/1e6, 6)),
		numCard("GAIN", fmt.Sprintf("%d", m.Gain)),
		numCard("OFFSET", fmt.Sprintf("%d", m.Offset)),
		numCard("XBINNING", fmt.Sprintf("%d", maxInt(1, m.Bin))),
		numCard("YBINNING", fmt.Sprintf("%d", maxInt(1, m.Bin))),
		strCard("SWCREATE", "astrostack"), // NOT "CREATOR" — see the contract note above
	}
	if f := strings.TrimSpace(m.Filter); f != "" {
		cards = append(cards, strCard("FILTER", f))
	}
	if m.HasTemp {
		cards = append(cards, numCard("CCD-TEMP", trimFloat(float64(m.TempMilliC)/1000, 2)))
	}
	if m.HasTargetTemp {
		cards = append(cards, numCard("SET-TEMP", trimFloat(m.TargetTempC, 2)))
	}
	if o := strings.TrimSpace(m.Object); o != "" {
		cards = append(cards, strCard("OBJECT", o))
	}
	if v := strings.TrimSpace(m.Instrume); v != "" {
		cards = append(cards, strCard("INSTRUME", v))
	}
	if v := strings.TrimSpace(m.Telescop); v != "" {
		cards = append(cards, strCard("TELESCOP", v))
	}
	if m.FocalLenMM > 0 {
		cards = append(cards, numCard("FOCALLEN", trimFloat(m.FocalLenMM, 2)))
	}
	if m.PixelSizeUm > 0 {
		cards = append(cards,
			numCard("XPIXSZ", trimFloat(m.PixelSizeUm, 3)),
			numCard("YPIXSZ", trimFloat(m.PixelSizeUm, 3)))
	}
	if m.HasCoord {
		cards = append(cards,
			strCard("OBJCTRA", RAString(m.RADeg)),
			strCard("OBJCTDEC", DecString(m.DecDeg)))
	}
	if p := strings.TrimSpace(m.Panel); p != "" {
		cards = append(cards, strCard("PANEL", p))
	}
	if s := strings.TrimSpace(m.SessionID); s != "" {
		cards = append(cards, strCard("SESSION", s))
	}
	if !m.StartedAt.IsZero() {
		cards = append(cards, strCard("DATE-OBS", DateObs(m.StartedAt)))
	}
	return cards
}

// DateObs formats an exposure start the ONLY way inspect can parse it: no zone suffix, no offset.
// The instant is rendered in its own location, so the local-noon night key lands on the right night.
func DateObs(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000")
}

// RAString renders right ascension as sexagesimal hours ("00 42 44.30"), the OBJCTRA convention.
func RAString(raDeg float64) string {
	hours := math.Mod(raDeg/15, 24)
	if hours < 0 {
		hours += 24
	}
	h := int(hours)
	m := int((hours - float64(h)) * 60)
	s := (hours-float64(h))*3600 - float64(m)*60
	return fmt.Sprintf("%02d %02d %05.2f", h, m, s)
}

// DecString renders declination as sexagesimal degrees with an explicit sign ("+41 16 09.0").
func DecString(decDeg float64) string {
	sign := "+"
	if decDeg < 0 {
		sign = "-"
		decDeg = -decDeg
	}
	d := int(decDeg)
	m := int((decDeg - float64(d)) * 60)
	s := (decDeg-float64(d))*3600 - float64(m)*60
	return fmt.Sprintf("%s%02d %02d %04.1f", sign, d, m, s)
}

// processedTokens are the words inspect treats as "this is a processed output, not a capture"
// (internal/inspect/filename.go). A capture file whose name contains one is silently dropped from
// every scan, so FileName must never emit one.
var processedTokens = []string{
	"stacked", "stack", "master", "combined", "final", "mosaic",
	"annotated", "preview", "thumb", "starless", "autosave",
}

// FileName builds a capture file name in the canonical form inspect can parse even with no header
// at all — the belt-and-braces copy of the metadata:
//
//	Light_30sec_Bin1_filter-Ha_-15.0C_gain139_2026-07-27_221403_frame0001.fit
func (m FrameMeta) FileName(seq int) string {
	parts := []string{titleType(m.Type)}
	// SIX decimals, which is exactly one microsecond. Three lost the whole short end: an ASI's
	// minimum exposure is 32 µs, and at three decimals every frame from a bias set to a lucky-imaging
	// burst was named "0sec" — a name that flatly contradicts the EXPTIME beside it, in a scheme
	// whose entire point is being a second, independent copy of the header. trimFloat still drops
	// trailing zeros, so 30 s stays "30sec" and 0.2 s stays "0.2sec".
	parts = append(parts, fmt.Sprintf("%ssec", trimFloat(float64(m.ExposureUs)/1e6, 6)))
	parts = append(parts, fmt.Sprintf("Bin%d", maxInt(1, m.Bin)))
	if f := sanitizeToken(m.Filter); f != "" {
		parts = append(parts, "filter-"+f)
	}
	if m.HasTemp {
		parts = append(parts, fmt.Sprintf("%sC", trimFloat(float64(m.TempMilliC)/1000, 1)))
	}
	parts = append(parts, fmt.Sprintf("gain%d", m.Gain))
	stamp := m.StartedAt
	if stamp.IsZero() {
		stamp = time.Now()
	}
	parts = append(parts, stamp.Format("2006-01-02"), stamp.Format("150405"))
	parts = append(parts, fmt.Sprintf("frame%04d", seq))
	return strings.Join(parts, "_") + ".fit"
}

// titleType is the filename prefix inspect matches on ("light…", "dark…", …).
func titleType(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "dark":
		return "Dark"
	case "flat":
		return "Flat"
	case "bias", "offset", "zero":
		return "Bias"
	case "darkflat", "dark_flat", "flatdark":
		return "FlatDark"
	default:
		return "Light"
	}
}

// SanitizeToken is sanitizeToken for callers building a PATH segment out of the same metadata (the
// capture layout puts the filter in the folder name as well as the file name). Same guarantees:
// [A-Za-z0-9] only, and empty for anything inspect would read as processed output.
func SanitizeToken(s string) string { return sanitizeToken(s) }

// sanitizeToken keeps a filename token to [A-Za-z0-9] so it cannot inject separators — or, worse, a
// token that makes inspect classify the frame as processed output.
func sanitizeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, bad := range processedTokens {
		if strings.EqualFold(out, bad) {
			return ""
		}
	}
	return out
}

func trimFloat(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// strCard/numCard mirror internal/fits's private card builders: FITS wants quoted strings
// left-aligned in 20 columns and numerics right-aligned.
func strCard(key, val string) string {
	return pad80(fmt.Sprintf("%-8s= %-20s", key, "'"+val+"'"))
}

func numCard(key, val string) string {
	return pad80(fmt.Sprintf("%-8s= %20s", key, val))
}

func pad80(s string) string {
	if len(s) >= 80 {
		return s[:80]
	}
	return s + strings.Repeat(" ", 80-len(s))
}

// FloatCard builds a numeric FITS card with a comment, for header values a DRIVER knows and the
// standard capture metadata has no field for. It is exported because those values are written by the
// driver packages, not by this one — see Frame.ExtraCards.
func FloatCard(key string, v float64, comment string) string {
	card := fmt.Sprintf("%-8s= %20s", key, strconv.FormatFloat(v, 'f', -1, 64))
	if comment != "" {
		card += " / " + comment
	}
	return pad80(card)
}
