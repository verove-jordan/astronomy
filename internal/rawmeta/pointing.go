// Pointing metadata: where the camera stood and which way it faced. The site and the compass
// bearing come from the GPS sub-IFD; the tilt and roll come from the gravity vector Apple writes
// into its MakerNote. Together they place a hand-framed phone shot on the sky to about a degree,
// which is the difference between guessing how a night's frames group and knowing.
//
// Everything here soft-fails to "absent" like the rest of the package: a non-Apple raw, a future
// MakerNote layout, or a photo with location services off all report Has* = false rather than a
// plausible-looking wrong answer.
package rawmeta

import (
	"encoding/binary"
	"math"
	"os"
	"strings"
)

// GPS sub-IFD tag numbers (Exif 2.3 §4.6.6).
const (
	tagGPSIFD          = 0x8825 // LONG pointer to the GPS sub-IFD, in IFD0
	tagGPSLatitudeRef  = 0x0001 // ASCII "N" or "S"
	tagGPSLatitude     = 0x0002 // 3 RATIONALs: degrees, minutes, seconds
	tagGPSLongitudeRef = 0x0003 // ASCII "E" or "W"
	tagGPSLongitude    = 0x0004 // 3 RATIONALs
	tagGPSImgDirection = 0x0011 // RATIONAL, degrees clockwise from north
)

// Apple MakerNote, reached through tag 0x927C in the Exif IFD. The block opens with the ASCII
// header, two version bytes and a two-byte order mark, putting its IFD entry count 14 bytes in.
const (
	tagMakerNote            = 0x927C
	appleAccelerationVector = 0x0008 // 3 SRATIONALs: the gravity vector in device axes, in g
	appleHeader             = "Apple iOS\x00"
	appleIFDOffset          = 14
	appleOrderOffset        = 12
)

// gravityTolerance bounds the accepted magnitude of the gravity vector. The value is gravity, so it
// must come out near 1 g; a MakerNote laid out differently from what we parse produces arbitrary
// rationals, and this is what catches that instead of turning it into a wrong camera altitude.
const (
	gravityMin = 0.8
	gravityMax = 1.25
)

// readGPS fills the capture site and the compass bearing from the GPS sub-IFD at offset.
func readGPS(f *os.File, bo binary.ByteOrder, offset int64, m *Meta) {
	entries := readIFD(f, bo, offset)
	lat, latOK := gpsDegrees(entries, f, bo, tagGPSLatitude, tagGPSLatitudeRef, "S")
	lon, lonOK := gpsDegrees(entries, f, bo, tagGPSLongitude, tagGPSLongitudeRef, "W")
	if latOK && lonOK {
		m.LatDeg, m.LonDeg, m.HasSite = lat, lon, true
	}
	if e, ok := findTag(entries, tagGPSImgDirection); ok {
		if v, ok := e.rationals(f, bo, 0, 1); ok {
			m.CompassDeg, m.HasCompass = math.Mod(math.Mod(v[0], 360)+360, 360), true
		}
	}
}

// gpsDegrees reads one degrees/minutes/seconds triple and its hemisphere reference, returning
// signed decimal degrees. negRef is the reference letter that makes the value negative ("S" for
// latitude, "W" for longitude).
func gpsDegrees(entries []ifdEntry, f *os.File, bo binary.ByteOrder, valTag, refTag uint16, negRef string) (float64, bool) {
	e, ok := findTag(entries, valTag)
	if !ok {
		return 0, false
	}
	dms, ok := e.rationals(f, bo, 0, 3)
	if !ok {
		return 0, false
	}
	deg := dms[0] + dms[1]/60 + dms[2]/3600
	if ref, ok := findTag(entries, refTag); ok {
		if strings.EqualFold(strings.TrimSpace(ref.ascii(f, bo)), negRef) {
			deg = -deg
		}
	}
	return deg, true
}

// readAppleGravity parses the AccelerationVector out of the Apple MakerNote block starting at
// start (a file offset, since the MakerNote is always larger than the 4-byte inline value field).
func readAppleGravity(f *os.File, start int64, m *Meta) {
	head := make([]byte, appleIFDOffset)
	if _, err := f.ReadAt(head, start); err != nil {
		return
	}
	if string(head[:len(appleHeader)]) != appleHeader {
		return // another vendor's MakerNote in the same tag
	}
	bo, ok := byteOrder(head[appleOrderOffset:])
	if !ok {
		return
	}
	e, ok := findTag(readIFD(f, bo, start+appleIFDOffset), appleAccelerationVector)
	if !ok {
		return
	}
	// Offsets inside the MakerNote are relative to the first byte of the block, NOT to the start of
	// the TIFF the way every other IFD offset in the file is. That base was measured against real
	// iPhone 16 Pro files rather than taken on trust, and the magnitude check below is what keeps a
	// future layout change from silently shifting it.
	v, ok := e.rationals(f, bo, start, 3)
	if !ok {
		return
	}
	if n := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2]); n < gravityMin || n > gravityMax {
		return
	}
	m.Gravity, m.HasGravity = [3]float64{v[0], v[1], v[2]}, true
}

// rationals reads count RATIONAL or SRATIONAL values from the entry. base is added to the offset
// held in the value field: 0 for a normal TIFF IFD, and the MakerNote block start inside an Apple
// MakerNote, whose offsets are relative to itself.
func (e ifdEntry) rationals(f *os.File, bo binary.ByteOrder, base int64, count int) ([]float64, bool) {
	if e.typ != typeRational && e.typ != typeSRational {
		return nil, false
	}
	if int(e.count) < count || count <= 0 {
		return nil, false
	}
	buf := make([]byte, count*8)
	if _, err := f.ReadAt(buf, base+int64(bo.Uint32(e.val[0:4]))); err != nil {
		return nil, false
	}
	out := make([]float64, count)
	for i := range out {
		num, den := bo.Uint32(buf[i*8:i*8+4]), bo.Uint32(buf[i*8+4:i*8+8])
		if den == 0 {
			return nil, false
		}
		if e.typ == typeSRational {
			out[i] = float64(int32(num)) / float64(int32(den))
			continue
		}
		out[i] = float64(num) / float64(den)
	}
	return out, true
}
