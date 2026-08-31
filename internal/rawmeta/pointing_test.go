package rawmeta

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rationals below are the ones a real iPhone 16 Pro wrote for a frame of the 2026-08-11 Milky
// Way session: 47°16'36.57"N 2°29'37.93"W, bearing 215.207°, gravity (0.0268, -0.7756, 0.6358).
// The bytes are rebuilt here rather than shipped as a 17 MB fixture, which also lets the table
// exercise hemispheres and malformed MakerNotes that no single real file could cover.
var (
	latDMS  = []rational{{47, 1}, {16, 1}, {3657, 100}}
	lonDMS  = []rational{{2, 1}, {29, 1}, {3793, 100}}
	bearing = []rational{{2152068, 10000}}
	gravity = []rational{{2355, 87956}, {-15840, 20423}, {10784, 16961}}
)

func TestParseTIFF_Pointing(t *testing.T) {
	tests := []struct {
		name         string
		build        func(*tiffBuilder)
		wantSite     bool
		wantLat      float64
		wantLon      float64
		wantCompass  bool
		wantBearing  float64
		wantGravity  bool
		wantGravityX float64
	}{
		{
			name:         "iphone frame carries site, bearing and gravity",
			build:        func(b *tiffBuilder) { b.gps("N", "W"); b.appleMakerNote(appleHeader, gravity) },
			wantSite:     true,
			wantLat:      47.276825,
			wantLon:      -2.4938694,
			wantCompass:  true,
			wantBearing:  215.2068,
			wantGravity:  true,
			wantGravityX: 0.026774751,
		},
		{
			name:        "southern and eastern hemispheres flip the signs",
			build:       func(b *tiffBuilder) { b.gps("S", "E") },
			wantSite:    true,
			wantLat:     -47.276825,
			wantLon:     2.4938694,
			wantCompass: true,
			wantBearing: 215.2068,
		},
		{
			name:  "no GPS IFD leaves the site absent rather than at null island",
			build: func(b *tiffBuilder) { b.appleMakerNote(appleHeader, gravity) },
			// A camera with location services off must not report 0,0 — that is a real place in the
			// Gulf of Guinea and would put every frame's altitude ~47 degrees wrong.
			wantGravity:  true,
			wantGravityX: 0.026774751,
		},
		{
			name:        "another vendor's MakerNote in the same tag is ignored",
			build:       func(b *tiffBuilder) { b.gps("N", "W"); b.appleMakerNote("Nikon\x00\x00\x00\x00\x00", gravity) },
			wantSite:    true,
			wantLat:     47.276825,
			wantLon:     -2.4938694,
			wantCompass: true,
			wantBearing: 215.2068,
		},
		{
			name: "gravity that is not 1g is rejected, not believed",
			// This is the guard against a future MakerNote layout shifting the offset base: garbage
			// rationals fail the magnitude check instead of becoming a confident wrong altitude.
			build: func(b *tiffBuilder) {
				b.gps("N", "W")
				b.appleMakerNote(appleHeader, []rational{{5, 1}, {5, 1}, {5, 1}})
			},
			wantSite:    true,
			wantLat:     47.276825,
			wantLon:     -2.4938694,
			wantCompass: true,
			wantBearing: 215.2068,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTIFFBuilder()
			tt.build(b)
			path := filepath.Join(t.TempDir(), "frame.dng")
			require.NoError(t, os.WriteFile(path, b.bytes(), 0o644))

			m := parseTIFF(path)

			assert.Equal(t, tt.wantSite, m.HasSite, "HasSite")
			assert.InDelta(t, tt.wantLat, m.LatDeg, 1e-6, "LatDeg")
			assert.InDelta(t, tt.wantLon, m.LonDeg, 1e-6, "LonDeg")
			assert.Equal(t, tt.wantCompass, m.HasCompass, "HasCompass")
			assert.InDelta(t, tt.wantBearing, m.CompassDeg, 1e-6, "CompassDeg")
			assert.Equal(t, tt.wantGravity, m.HasGravity, "HasGravity")
			assert.InDelta(t, tt.wantGravityX, m.Gravity[0], 1e-8, "Gravity X")
		})
	}
}

// TestParseTIFF_PointingKeepsCoreMeta pins that adding the pointing tags did not disturb the
// metadata the rest of the engine already relies on.
func TestParseTIFF_PointingKeepsCoreMeta(t *testing.T) {
	b := newTIFFBuilder()
	b.gps("N", "W")
	b.appleMakerNote(appleHeader, gravity)
	path := filepath.Join(t.TempDir(), "frame.dng")
	require.NoError(t, os.WriteFile(path, b.bytes(), 0o644))

	m := parseTIFF(path)

	assert.Equal(t, int64(2500), m.ISO)
	assert.True(t, m.HasISO)
	assert.Equal(t, int64(10000), m.ExposureMs)
	assert.True(t, m.HasExposure)
	assert.Equal(t, 4032, m.Width)
	assert.Equal(t, 3024, m.Height)
	assert.Equal(t, 6, m.Orientation)
	assert.Equal(t, "iPhone 16 Pro", m.CameraModel)
}

// --- minimal TIFF writer -------------------------------------------------------------------
//
// Builds a little-endian TIFF with IFD0 -> {Exif IFD, GPS IFD} and an optional Apple MakerNote,
// which is itself a big-endian IFD whose internal offsets are relative to its own first byte.

type rational struct{ num, den int32 }

type tiffBuilder struct {
	data       []byte // the overflow area; offsets into it are absolute file offsets
	gpsEntries []ifdRaw
	makerNote  []byte
}

type ifdRaw struct {
	tag, typ uint16
	count    uint32
	val      [4]byte
}

// tiffHeaderLen is the 8-byte "II*\0" header; the overflow area starts right after it, and the
// three IFDs are appended last so their data offsets are already known.
const tiffHeaderLen = 8

func newTIFFBuilder() *tiffBuilder {
	return &tiffBuilder{data: make([]byte, tiffHeaderLen)}
}

// put appends raw bytes to the overflow area and returns their absolute offset.
func (b *tiffBuilder) put(p []byte) uint32 {
	off := uint32(len(b.data))
	b.data = append(b.data, p...)
	return off
}

func (b *tiffBuilder) putRationals(bo binary.ByteOrder, vals []rational) uint32 {
	buf := make([]byte, 0, len(vals)*8)
	for _, v := range vals {
		buf = appendU32(buf, bo, uint32(v.num))
		buf = appendU32(buf, bo, uint32(v.den))
	}
	return b.put(buf)
}

func (b *tiffBuilder) gps(latRef, lonRef string) {
	le := binary.LittleEndian
	b.gpsEntries = []ifdRaw{
		asciiEntry(tagGPSLatitudeRef, latRef),
		offsetEntry(le, tagGPSLatitude, typeRational, 3, b.putRationals(le, latDMS)),
		asciiEntry(tagGPSLongitudeRef, lonRef),
		offsetEntry(le, tagGPSLongitude, typeRational, 3, b.putRationals(le, lonDMS)),
		offsetEntry(le, tagGPSImgDirection, typeRational, 1, b.putRationals(le, bearing)),
	}
}

// appleMakerNote assembles the MakerNote block: header, version, "MM" order mark, then a
// big-endian IFD holding AccelerationVector whose value offset is relative to the block start.
func (b *tiffBuilder) appleMakerNote(header string, accel []rational) {
	be := binary.BigEndian
	const entryCount = 1
	ifdLen := 2 + entryCount*12 + 4
	accelOffset := uint32(appleIFDOffset + ifdLen)

	blk := make([]byte, 0, int(accelOffset)+len(accel)*8)
	blk = append(blk, header...)
	blk = append(blk, 0x00, 0x01, 'M', 'M') // version, then the byte-order mark
	blk = appendU16(blk, be, entryCount)
	blk = appendEntry(blk, be, offsetEntry(be, appleAccelerationVector, typeSRational, uint32(len(accel)), accelOffset))
	blk = appendU32(blk, be, 0) // no next IFD
	for _, v := range accel {
		blk = appendU32(blk, be, uint32(v.num))
		blk = appendU32(blk, be, uint32(v.den))
	}
	b.makerNote = blk
}

func (b *tiffBuilder) bytes() []byte {
	le := binary.LittleEndian
	modelOff := b.put(append([]byte("iPhone 16 Pro"), 0))
	exposureOff := b.putRationals(le, []rational{{10, 1}})

	exif := []ifdRaw{
		shortEntry(tagISOSpeed, 2500),
		offsetEntry(le, tagExposureTime, typeRational, 1, exposureOff),
		longEntry(tagPixelXDimension, 4032),
		longEntry(tagPixelYDimension, 3024),
	}
	if len(b.makerNote) > 0 {
		exif = append(exif, offsetEntry(le, tagMakerNote, 7 /* UNDEFINED */, uint32(len(b.makerNote)), b.put(b.makerNote)))
	}

	exifOff := b.appendIFD(le, exif)
	ifd0 := []ifdRaw{
		shortEntry(tagOrientation, 6),
		offsetEntry(le, tagModel, typeASCII, uint32(len("iPhone 16 Pro")+1), modelOff),
		offsetEntry(le, tagExifIFD, typeLong, 1, exifOff),
	}
	if len(b.gpsEntries) > 0 {
		ifd0 = append(ifd0, offsetEntry(le, tagGPSIFD, typeLong, 1, b.appendIFD(le, b.gpsEntries)))
	}
	ifd0Off := b.appendIFD(le, ifd0)

	copy(b.data[0:4], []byte{'I', 'I', 42, 0})
	le.PutUint32(b.data[4:8], ifd0Off)
	return b.data
}

// appendIFD writes an IFD (entries must already be tag-ordered per the TIFF spec) and returns its
// absolute offset.
func (b *tiffBuilder) appendIFD(bo binary.ByteOrder, entries []ifdRaw) uint32 {
	buf := appendU16(nil, bo, uint16(len(entries)))
	for _, e := range entries {
		buf = appendEntry(buf, bo, e)
	}
	buf = appendU32(buf, bo, 0) // no next IFD
	return b.put(buf)
}

func appendEntry(dst []byte, bo binary.ByteOrder, e ifdRaw) []byte {
	dst = appendU16(dst, bo, e.tag)
	dst = appendU16(dst, bo, e.typ)
	dst = appendU32(dst, bo, e.count)
	return append(dst, e.val[:]...)
}

func appendU16(dst []byte, bo binary.ByteOrder, v uint16) []byte {
	var b [2]byte
	bo.PutUint16(b[:], v)
	return append(dst, b[:]...)
}

func appendU32(dst []byte, bo binary.ByteOrder, v uint32) []byte {
	var b [4]byte
	bo.PutUint32(b[:], v)
	return append(dst, b[:]...)
}

func offsetEntry(bo binary.ByteOrder, tag, typ uint16, count, offset uint32) ifdRaw {
	e := ifdRaw{tag: tag, typ: typ, count: count}
	bo.PutUint32(e.val[:], offset)
	return e
}

// shortEntry and longEntry inline their value little-endian: they are only used to build the outer
// TIFF, never the big-endian MakerNote IFD, which goes through offsetEntry with an explicit order.
func shortEntry(tag uint16, v uint16) ifdRaw {
	e := ifdRaw{tag: tag, typ: typeShort, count: 1}
	binary.LittleEndian.PutUint16(e.val[0:2], v)
	return e
}

func longEntry(tag uint16, v uint32) ifdRaw {
	e := ifdRaw{tag: tag, typ: typeLong, count: 1}
	binary.LittleEndian.PutUint32(e.val[:], v)
	return e
}

// asciiEntry inlines a short string in the value field, which is where a 1-character GPS
// hemisphere reference always lives.
func asciiEntry(tag uint16, s string) ifdRaw {
	e := ifdRaw{tag: tag, typ: typeASCII, count: uint32(len(s) + 1)}
	copy(e.val[:], s)
	return e
}
