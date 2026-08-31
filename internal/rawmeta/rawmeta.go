// Package rawmeta reads camera metadata (ISO, exposure, model, pixel dimensions, orientation) from
// image files — primarily iPhone/DSLR raws (DNG) and TIFFs — WITHOUT decoding the pixels. It parses
// the TIFF/EXIF IFD directly, which is the reliable path for DNG (where macOS `sips -g` reports
// <nil>), and falls back to Spotlight metadata (`mdls`) for non-TIFF containers such as HEIC. Every
// field soft-fails to its zero value (no mdls, non-macOS, or an absent tag) so callers degrade
// gracefully — a phone capture with no readable metadata simply gets ISO 0 and is treated as such.
package rawmeta

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// EXIF/TIFF tag numbers we read (see the TIFF 6.0 and Exif specs).
const (
	tagModel            = 0x0110 // ASCII camera model, in IFD0
	tagOrientation      = 0x0112 // SHORT 1..8, in IFD0
	tagExifIFD          = 0x8769 // LONG pointer to the Exif sub-IFD
	tagISOSpeed         = 0x8827 // SHORT ISO speed rating(s), in the Exif IFD
	tagExposureTime     = 0x829A // RATIONAL seconds, in the Exif IFD
	tagDateTimeOriginal = 0x9003 // ASCII "YYYY:MM:DD HH:MM:SS", in the Exif IFD
	tagOffsetTimeOrig   = 0x9011 // ASCII "+HH:MM" UTC offset for tagDateTimeOriginal
	tagPixelXDimension  = 0xA002 // SHORT/LONG main-image width, in the Exif IFD
	tagPixelYDimension  = 0xA003 // SHORT/LONG main-image height, in the Exif IFD
	tagFocalLength35mm  = 0xA405 // SHORT 35mm-equivalent focal length, in the Exif IFD
	tagSubIFDs          = 0x014A // LONG offsets to the DNG sub-images, in IFD0
	tagPhotometric      = 0x0106 // SHORT photometric interpretation
)

// photometricLinearRaw marks a DNG whose full-resolution image is already demosaiced ("linear DNG",
// DNG spec 1.4 §4). Apple ProRAW is written this way.
const photometricLinearRaw = 34892

// exifDateTime is the Exif DateTimeOriginal layout; exifOffset is the OffsetTimeOriginal layout.
const (
	exifDateTime = "2006:01:02 15:04:05"
	exifOffset   = "-07:00"
)

// TIFF field types we handle.
const (
	typeASCII     = 2
	typeShort     = 3
	typeLong      = 4
	typeRational  = 5
	typeSRational = 10
)

// mdlsTimeout bounds each Spotlight query so a slow/absent `mdls` never stalls a scan.
const mdlsTimeout = 5 * time.Second

// Meta is the camera metadata recovered from a file. Unset fields are zero; HasISO/HasExposure
// distinguish a genuine zero from "absent" so callers can tell "no metadata" from "ISO reported 0".
type Meta struct {
	ISO         int64
	ExposureMs  int64
	CameraModel string
	Width       int
	Height      int
	Orientation int // EXIF orientation code 1..8, 0 when absent
	HasISO      bool
	HasExposure bool
	// FocalLength35mm is the 35mm-equivalent focal length (0 when absent). On a phone it encodes the
	// digital-zoom factor, which together with Width is the metadata PRIOR for image scale — the
	// solar triage uses it to label groups, never to key them (the measured disc radius does that).
	FocalLength35mm int
	// TakenAtMs is the capture instant in Unix milliseconds (0 when absent). Only set when the
	// timezone is unambiguous — Exif DateTimeOriginal alone is local-time-without-offset, so it is
	// used only alongside OffsetTimeOriginal; otherwise the mdls fallback (which carries an offset)
	// fills it. Mixing the two conventions would silently mis-order a DNG against a HEIC.
	TakenAtMs int64
	// LatDeg/LonDeg are the capture site in signed decimal degrees, north and east positive.
	LatDeg float64
	LonDeg float64
	// CompassDeg is the camera's horizontal bearing, degrees clockwise from true north.
	CompassDeg float64
	// Gravity is the gravity vector in device axes, in g, as Apple records it. Its z component gives
	// the camera's altitude above the horizon and its x/y give the roll — see internal/pointing,
	// which owns that conversion. Only Apple writes it; everything else reports HasGravity false.
	Gravity    [3]float64
	HasSite    bool
	HasCompass bool
	HasGravity bool
	// LinearRaw marks a DNG whose full-resolution image is already demosaiced — a "linear DNG", which
	// is how Apple writes ProRAW. It matters for calibration: such a file has already had its black
	// level removed and its gain normalised, so two frames of the same scene at ISO 2500 and ISO 6400
	// come out at the SAME pixel level (measured: 4% apart, not the 2.56x the ISO ratio implies).
	// Keying a dark master on exact ISO is therefore wrong for these files — see internal/calib.
	LinearRaw bool
}

// Read returns the metadata for path. It first parses the TIFF/EXIF IFDs directly (works for DNG and
// TIFF), then fills any still-missing field from macOS `mdls` (the only path for HEIC and other
// non-TIFF containers). Returns a zero Meta when nothing is readable.
func Read(path string) Meta {
	m := parseTIFF(path)
	if !m.HasISO {
		if v := mdlsFloat(path, "kMDItemISOSpeed"); v > 0 {
			m.ISO, m.HasISO = int64(math.Round(v)), true
		}
	}
	if !m.HasExposure {
		if v := mdlsFloat(path, "kMDItemExposureTimeSeconds"); v > 0 {
			m.ExposureMs, m.HasExposure = int64(math.Round(v*1000)), true
		}
	}
	if m.CameraModel == "" {
		m.CameraModel = mdlsString(path, "kMDItemAcquisitionModel")
	}
	if m.Width == 0 {
		m.Width = int(mdlsFloat(path, "kMDItemPixelWidth"))
	}
	if m.Height == 0 {
		m.Height = int(mdlsFloat(path, "kMDItemPixelHeight"))
	}
	if m.FocalLength35mm == 0 {
		m.FocalLength35mm = int(mdlsFloat(path, "kMDItemFocalLength35mm"))
	}
	if m.TakenAtMs == 0 {
		m.TakenAtMs = mdlsTime(path, "kMDItemContentCreationDate")
	}
	return m
}

// mdlsTimeFormats are the layouts `mdls` prints dates in (it is locale-independent for these keys,
// but the fractional-seconds form appears on some volumes).
var mdlsTimeFormats = []string{"2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05.999999 -0700"}

// mdlsTime reads a Spotlight date attribute as Unix milliseconds (0 when absent or unparsable).
func mdlsTime(path, attr string) int64 {
	raw := strings.Trim(mdlsString(path, attr), `"`)
	if raw == "" {
		return 0
	}
	for _, layout := range mdlsTimeFormats {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// parseTIFF reads IFD0 (model, orientation) and follows the Exif sub-IFD pointer (ISO, exposure,
// dimensions). It reads only the header and the two small IFDs via bounded ReadAt calls, so it stays
// cheap even on a multi-MB raw. A non-TIFF container returns a zero Meta (the mdls fallback covers it).
func parseTIFF(path string) Meta {
	var m Meta
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()

	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return m
	}
	bo, ok := byteOrder(head)
	if !ok {
		return m // not a TIFF container (e.g. HEIC)
	}
	ifd0 := int64(bo.Uint32(head[4:8]))
	entries := readIFD(f, bo, ifd0)
	if e, ok := findTag(entries, tagOrientation); ok {
		m.Orientation = int(e.scalar(bo))
	}
	if e, ok := findTag(entries, tagModel); ok {
		m.CameraModel = e.ascii(f, bo)
	}
	if e, ok := findTag(entries, tagExifIFD); ok {
		readExif(f, bo, int64(bo.Uint32(e.val[0:4])), &m)
	}
	if e, ok := findTag(entries, tagGPSIFD); ok {
		readGPS(f, bo, int64(bo.Uint32(e.val[0:4])), &m)
	}
	m.LinearRaw = linearRaw(f, bo, entries)
	return m
}

// linearRaw reports whether any of the DNG's sub-images is an already-demosaiced "linear raw". The
// full-resolution image lives in a SubIFD, not IFD0, so the flag cannot be read without following
// that pointer.
func linearRaw(f *os.File, bo binary.ByteOrder, entries []ifdEntry) bool {
	e, ok := findTag(entries, tagSubIFDs)
	if !ok {
		return false
	}
	offsets, ok := e.longs(f, bo)
	if !ok {
		return false
	}
	for _, off := range offsets {
		sub := readIFD(f, bo, int64(off))
		if p, ok := findTag(sub, tagPhotometric); ok && p.scalar(bo) == photometricLinearRaw {
			return true
		}
	}
	return false
}

// longs reads a LONG array, inline when it is a single value and from its offset otherwise.
func (e ifdEntry) longs(f *os.File, bo binary.ByteOrder) ([]uint32, bool) {
	if e.typ != typeLong || e.count == 0 || e.count > 64 {
		return nil, false
	}
	if e.count == 1 {
		return []uint32{bo.Uint32(e.val[0:4])}, true
	}
	buf := make([]byte, e.count*4)
	if _, err := f.ReadAt(buf, int64(bo.Uint32(e.val[0:4]))); err != nil {
		return nil, false
	}
	out := make([]uint32, e.count)
	for i := range out {
		out[i] = bo.Uint32(buf[i*4 : i*4+4])
	}
	return out, true
}

// readExif reads the ISO, exposure and pixel dimensions from the Exif sub-IFD at offset.
func readExif(f *os.File, bo binary.ByteOrder, offset int64, m *Meta) {
	entries := readIFD(f, bo, offset)
	if e, ok := findTag(entries, tagISOSpeed); ok { // may be an array — take the first
		if iso := e.scalar(bo); iso > 0 {
			m.ISO, m.HasISO = iso, true
		}
	}
	if e, ok := findTag(entries, tagExposureTime); ok {
		if ms, ok := e.rationalMs(f, bo); ok {
			m.ExposureMs, m.HasExposure = ms, true
		}
	}
	if e, ok := findTag(entries, tagPixelXDimension); ok {
		m.Width = int(e.scalar(bo))
	}
	if e, ok := findTag(entries, tagPixelYDimension); ok {
		m.Height = int(e.scalar(bo))
	}
	if e, ok := findTag(entries, tagFocalLength35mm); ok {
		m.FocalLength35mm = int(e.scalar(bo))
	}
	if e, ok := findTag(entries, tagMakerNote); ok {
		readAppleGravity(f, int64(bo.Uint32(e.val[0:4])), m)
	}
	m.TakenAtMs = exifTakenAt(entries, f, bo)
}

// exifTakenAt resolves DateTimeOriginal to Unix milliseconds, but ONLY when OffsetTimeOriginal is
// present — DateTimeOriginal carries no timezone, so assuming one would mis-order files against the
// mdls path (which is offset-aware). Returns 0 to let that fallback take over.
func exifTakenAt(entries []ifdEntry, f *os.File, bo binary.ByteOrder) int64 {
	dt, ok := findTag(entries, tagDateTimeOriginal)
	if !ok {
		return 0
	}
	off, ok := findTag(entries, tagOffsetTimeOrig)
	if !ok {
		return 0
	}
	loc, err := time.Parse(exifOffset, strings.TrimSpace(off.ascii(f, bo)))
	if err != nil {
		return 0
	}
	t, err := time.ParseInLocation(exifDateTime, strings.TrimSpace(dt.ascii(f, bo)), loc.Location())
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// ifdEntry is one 12-byte IFD directory entry, with its 4-byte value/offset field kept verbatim.
type ifdEntry struct {
	tag   uint16
	typ   uint16
	count uint32
	val   []byte // the 4-byte inline value, or an offset to the real value when it doesn't fit
}

// readIFD reads the directory at offset and returns its entries, or nil on any read error or an
// implausible entry count.
func readIFD(f *os.File, bo binary.ByteOrder, offset int64) []ifdEntry {
	cnt := make([]byte, 2)
	if _, err := f.ReadAt(cnt, offset); err != nil {
		return nil
	}
	n := int(bo.Uint16(cnt))
	if n <= 0 || n > 4096 {
		return nil
	}
	raw := make([]byte, n*12)
	if _, err := f.ReadAt(raw, offset+2); err != nil {
		return nil
	}
	entries := make([]ifdEntry, 0, n)
	for i := 0; i < n; i++ {
		e := raw[i*12 : i*12+12]
		entries = append(entries, ifdEntry{
			tag:   bo.Uint16(e[0:2]),
			typ:   bo.Uint16(e[2:4]),
			count: bo.Uint32(e[4:8]),
			val:   append([]byte(nil), e[8:12]...),
		})
	}
	return entries
}

func findTag(entries []ifdEntry, tag uint16) (ifdEntry, bool) {
	for _, e := range entries {
		if e.tag == tag {
			return e, true
		}
	}
	return ifdEntry{}, false
}

// scalar returns the entry's first value as an integer (SHORT or LONG), left-justified in the value
// field per the TIFF spec.
func (e ifdEntry) scalar(bo binary.ByteOrder) int64 {
	if e.typ == typeLong {
		return int64(bo.Uint32(e.val[0:4]))
	}
	return int64(bo.Uint16(e.val[0:2])) // SHORT (and a sane default)
}

// rationalMs reads a RATIONAL (num/den, 8 bytes at the offset in the value field) and returns it in
// milliseconds. ok=false if the type is wrong, the read fails, or the denominator is zero.
func (e ifdEntry) rationalMs(f *os.File, bo binary.ByteOrder) (int64, bool) {
	if e.typ != typeRational {
		return 0, false
	}
	buf := make([]byte, 8)
	if _, err := f.ReadAt(buf, int64(bo.Uint32(e.val[0:4]))); err != nil {
		return 0, false
	}
	num, den := bo.Uint32(buf[0:4]), bo.Uint32(buf[4:8])
	if den == 0 {
		return 0, false
	}
	return int64(math.Round(float64(num) / float64(den) * 1000)), true
}

// ascii returns the entry's ASCII string value (inline when ≤4 bytes, else read from its offset),
// trimmed of the NUL terminator and padding.
func (e ifdEntry) ascii(f *os.File, bo binary.ByteOrder) string {
	if e.typ != typeASCII || e.count == 0 {
		return ""
	}
	n := int(e.count)
	raw := e.val[:min(n, 4)]
	if n > 4 {
		raw = make([]byte, n)
		if _, err := f.ReadAt(raw, int64(bo.Uint32(e.val[0:4]))); err != nil {
			return ""
		}
	}
	return strings.TrimRight(string(raw), "\x00 ")
}

func byteOrder(head []byte) (binary.ByteOrder, bool) {
	switch {
	case head[0] == 'I' && head[1] == 'I':
		return binary.LittleEndian, true
	case head[0] == 'M' && head[1] == 'M':
		return binary.BigEndian, true
	}
	return nil, false
}

// mdlsFloat reads one numeric Spotlight attribute (macOS), returning 0 on any failure.
func mdlsFloat(path, attr string) float64 {
	out, ok := mdlsRaw(path, attr)
	if !ok {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		return 0
	}
	return v
}

// mdlsString reads one text Spotlight attribute (macOS), returning "" on failure or a "(null)" tag.
func mdlsString(path, attr string) string {
	out, ok := mdlsRaw(path, attr)
	if !ok {
		return ""
	}
	s := strings.TrimSpace(out)
	if s == "(null)" {
		return ""
	}
	return s
}

func mdlsRaw(path, attr string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), mdlsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mdls", "-name", attr, "-raw", path).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
