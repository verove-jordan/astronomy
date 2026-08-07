package scene3d

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// scene3d.bin is a FIXED-WIDTH record file laid out so the browser can hand it straight to the GPU:
// header | records | name table. Every record is 32 bytes and every attribute sits at an offset its
// own type can address (floats on 4, the 16-bit fields on 2), which is exactly what
// gl.vertexAttribPointer requires — so the whole star field uploads as ONE buffer with no parsing,
// no per-star objects and no garbage.
//
// Layout, all little-endian:
//
//	 0  dirX   float32   unit direction in scene coordinates (basis X/Y/Z)
//	 4  dirY   float32
//	 8  dirZ   float32   positive in front of the observer
//	12  dist   float32   distance in parsec
//	16  r      uint8     the star's colour — its blackbody hue when the temperature is known
//	17  g      uint8
//	18  b      uint8
//	19  absMag int8      absolute V magnitude ×4; absMagUnknown when the catalogue had none
//	20  flags  uint8     depth source + identified + cluster member + velocity + physical colour
//	21  mag    uint8     apparent magnitude, (m+5)×8; magUnknown when the frame was not anchored
//	22  name   uint16    1-based index into the name table; 0 = anonymous
//	24  vx     int16     space velocity in scene coordinates, km/s ×10; 0 when unmeasured
//	26  vy     int16
//	28  vz     int16
//	30  src    uint16    index into the run's stars.json, so a hover can read the full catalogue row
//
// The format is versioned and self-describing: a reader meeting an unknown version or record size
// must refuse the file rather than misread it, exactly like internal/deepstars/format.go.
const (
	fileMagic   = "ASTRO3DS"
	fileVersion = 2
	headerSize  = 64
	recordSize  = 32
)

// Record field offsets — the decoder reads them out of a byte slice by hand, so the names are the
// contract.
const (
	offDirX   = 0
	offDirY   = 4
	offDirZ   = 8
	offDist   = 12
	offR      = 16
	offG      = 17
	offB      = 18
	offAbsMag = 19
	offFlags  = 20
	offMag    = 21
	offName   = 22
	offVX     = 24
	offVY     = 26
	offVZ     = 28
	offSrc    = 30
)

// velScale is how the velocity components are quantised: km/s ×10, so 0.1 km/s of resolution over
// ±3276 km/s — past any speed a star in this galaxy reaches, and fine enough that a slow-moving
// cluster member's arrow is not a staircase.
const velScale = 10

// Sentinels for the two quantised magnitudes, where every in-range value is meaningful.
//
// absMagUnknown is written and compared as a BYTE rather than as int8(-128), which is the same bit
// pattern: Go evaluates constant conversions at compile time and rejects int8(-128) → byte as an
// overflow. Encoding clamps real values to ±127 so nothing legitimate can collide with it.
const (
	absMagUnknown byte = 0x80 // int8 -128
	magUnknown    byte = 255
)

// Flag bits carried in the record's flags byte.
const (
	flagDepthMask      = 0x03 // DepthSource, as-is
	flagIdentified     = 0x04 // the star was matched to a catalogue entry
	flagClusterMember  = 0x08 // it belongs to a cluster whose distance this frame measured
	flagHasVelocity    = 0x10 // a real space velocity was measurable — proper motion and/or radial
	flagPhysicalColour = 0x20 // the colour is a blackbody hue, not the sampled pixel
)

var errBadFormat = errors.New("scene3d: unrecognised catalogue format")

// star is one encodable point. Built by scene.go, consumed only by the encoder.
type star struct {
	dir       vec3
	distPc    float64
	r, g, b   uint8
	absMag    float64
	hasAbsMag bool
	mag       float64
	hasMag    bool
	source    DepthSource
	flags     uint8
	nameIdx   uint16
	// vel is the space velocity in scene coordinates, km/s. Only meaningful with flagHasVelocity.
	vel vec3
	// srcIdx is the star's index in the run's stars.json. It costs two bytes and saves the viewer an
	// entire second copy of the catalogue: a hover reads the full row out of the annotation it has
	// already fetched, so nothing about a star has to be duplicated into this file.
	srcIdx uint16
}

// encodeRecord writes one star into a recordSize-byte slice. It owns every scale and sentinel so a
// decoder can mirror it exactly.
func encodeRecord(b []byte, s star) {
	putF32 := func(off int, v float64) {
		binary.LittleEndian.PutUint32(b[off:], math.Float32bits(float32(v)))
	}
	putF32(offDirX, s.dir.X)
	putF32(offDirY, s.dir.Y)
	putF32(offDirZ, s.dir.Z)
	putF32(offDist, s.distPc)
	b[offR], b[offG], b[offB] = s.r, s.g, s.b

	b[offAbsMag] = absMagUnknown
	if s.hasAbsMag {
		b[offAbsMag] = byte(int8(clampInt(int(math.Round(s.absMag*4)), -127, 127)))
	}
	b[offMag] = magUnknown
	if s.hasMag {
		// (m+5)×8 puts magnitude -5 at 0 and keeps 0.125 mag of resolution to magnitude 26.
		b[offMag] = byte(clampInt(int(math.Round((s.mag+5)*8)), 0, int(magUnknown)-1))
	}
	b[offFlags] = s.flags | uint8(s.source)&flagDepthMask
	binary.LittleEndian.PutUint16(b[offName:], s.nameIdx)

	putVel := func(off int, v float64) {
		q := clampInt(int(math.Round(v*velScale)), math.MinInt16, math.MaxInt16)
		binary.LittleEndian.PutUint16(b[off:], uint16(int16(q)))
	}
	putVel(offVX, s.vel.X)
	putVel(offVY, s.vel.Y)
	putVel(offVZ, s.vel.Z)
	binary.LittleEndian.PutUint16(b[offSrc:], s.srcIdx)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// writeBin serialises the star field. names is the 1-based name table the records index into.
func writeBin(w io.Writer, stars []star, names []string) error {
	hdr := make([]byte, headerSize)
	copy(hdr, fileMagic)
	binary.LittleEndian.PutUint16(hdr[8:], fileVersion)
	binary.LittleEndian.PutUint16(hdr[10:], recordSize)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(stars)))
	binary.LittleEndian.PutUint32(hdr[16:], headerSize)
	binary.LittleEndian.PutUint32(hdr[20:], uint32(headerSize+len(stars)*recordSize))
	if _, err := w.Write(hdr); err != nil {
		return fmt.Errorf("write scene3d header: %w", err)
	}

	buf := make([]byte, recordSize)
	for _, s := range stars {
		for i := range buf {
			buf[i] = 0
		}
		encodeRecord(buf, s)
		if _, err := w.Write(buf); err != nil {
			return fmt.Errorf("write scene3d record: %w", err)
		}
	}
	return writeStringTable(w, names)
}

// writeStringTable emits the name table: a uint32 count then each entry as uint16 length + bytes.
// Same shape as the deep star catalogue's tables, so the two formats read alike.
func writeStringTable(w io.Writer, vals []string) error {
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(vals)))
	if _, err := w.Write(n[:]); err != nil {
		return fmt.Errorf("write scene3d names: %w", err)
	}
	var l [2]byte
	for _, v := range vals {
		if len(v) > math.MaxUint16 {
			return fmt.Errorf("scene3d: name too long (%d bytes)", len(v))
		}
		binary.LittleEndian.PutUint16(l[:], uint16(len(v)))
		if _, err := w.Write(l[:]); err != nil {
			return fmt.Errorf("write scene3d names: %w", err)
		}
		if _, err := io.WriteString(w, v); err != nil {
			return fmt.Errorf("write scene3d names: %w", err)
		}
	}
	return nil
}

// decodeBin reads back a scene3d.bin. It exists for the round-trip test and for anything that needs
// to inspect a scene without a browser — the frontend has its own decoder in TypeScript, and the two
// are pinned to the same layout by this file's comment and by the tests on both sides.
func decodeBin(b []byte) ([]star, []string, error) {
	if len(b) < headerSize || string(b[:8]) != fileMagic {
		return nil, nil, errBadFormat
	}
	if v := binary.LittleEndian.Uint16(b[8:]); v != fileVersion {
		return nil, nil, fmt.Errorf("%w: version %d", errBadFormat, v)
	}
	if rs := binary.LittleEndian.Uint16(b[10:]); rs != recordSize {
		return nil, nil, fmt.Errorf("%w: record size %d", errBadFormat, rs)
	}
	count := int(binary.LittleEndian.Uint32(b[12:]))
	recOff := int(binary.LittleEndian.Uint32(b[16:]))
	strOff := int(binary.LittleEndian.Uint32(b[20:]))
	if recOff < headerSize || strOff < recOff+count*recordSize || strOff > len(b) {
		return nil, nil, errBadFormat
	}

	out := make([]star, 0, count)
	for i := 0; i < count; i++ {
		rec := b[recOff+i*recordSize : recOff+(i+1)*recordSize]
		f32 := func(off int) float64 {
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(rec[off:])))
		}
		s := star{
			dir:     vec3{f32(offDirX), f32(offDirY), f32(offDirZ)},
			distPc:  f32(offDist),
			r:       rec[offR],
			g:       rec[offG],
			b:       rec[offB],
			flags:   rec[offFlags],
			source:  DepthSource(rec[offFlags] & flagDepthMask),
			nameIdx: binary.LittleEndian.Uint16(rec[offName:]),
			srcIdx:  binary.LittleEndian.Uint16(rec[offSrc:]),
		}
		getVel := func(off int) float64 {
			return float64(int16(binary.LittleEndian.Uint16(rec[off:]))) / velScale
		}
		s.vel = vec3{getVel(offVX), getVel(offVY), getVel(offVZ)}
		if rec[offAbsMag] != absMagUnknown {
			s.absMag, s.hasAbsMag = float64(int8(rec[offAbsMag]))/4, true
		}
		if rec[offMag] != magUnknown {
			s.mag, s.hasMag = float64(rec[offMag])/8-5, true
		}
		out = append(out, s)
	}

	names, _, err := readStringTable(b[strOff:])
	if err != nil {
		return nil, nil, err
	}
	return out, names, nil
}

// readStringTable reads one table written by writeStringTable.
func readStringTable(b []byte) ([]string, int, error) {
	if len(b) < 4 {
		return nil, 0, errBadFormat
	}
	n := int(binary.LittleEndian.Uint32(b))
	if n < 0 || n > 1<<20 { // a corrupt length must not become a huge allocation
		return nil, 0, errBadFormat
	}
	out := make([]string, 0, n)
	off := 4
	for i := 0; i < n; i++ {
		if off+2 > len(b) {
			return nil, 0, errBadFormat
		}
		l := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		if off+l > len(b) {
			return nil, 0, errBadFormat
		}
		out = append(out, string(b[off:off+l]))
		off += l
	}
	return out, off, nil
}
