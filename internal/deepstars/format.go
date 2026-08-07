package deepstars

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

// The deep catalogue file (athyg_v32.bin) is a DEC-SORTED, FIXED-WIDTH record file. Both properties
// are load-bearing:
//
//   - fixed width lets a cone query binary-search the declination band with ReadAt and then read
//     only that slab, so a 2.5-million-star / ~130 MB catalogue costs a few hundred KB per query
//     instead of 130 MB of resident memory — the engine already spends its budget on Siril;
//   - dec-sorted is what makes that binary search possible, and declination (unlike RA) has no
//     wrap-around, so a band is always one contiguous range.
//
// Layout: header | string tables | records. All integers little-endian. Strings that repeat across
// millions of rows (proper name, spectral type, constellation) live in the tables and the records
// carry 1-based indices; 0 always means "absent".
//
// The format is versioned and self-describing: a reader that meets an unknown version or record
// size refuses the file rather than silently misreading it, and the caller falls back to the
// embedded catalogue.

const (
	fileMagic   = "ATHYGCAT"
	fileVersion = 1
	headerSize  = 64
	recordSize  = 52
)

// Record field offsets. Kept as named constants because the decoder reads them out of a byte slice
// by hand — there is no struct tag to keep honest, so the names are the contract.
const (
	offRA     = 0  // int32, micro-degrees (0 … 360e6)
	offDec    = 4  // int32, micro-degrees (-90e6 … 90e6)
	offDist   = 8  // float32, parsec; 0 = unknown
	offHD     = 12 // uint32, Henry Draper number; 0 = none
	offHIP    = 16 // uint32, Hipparcos number; 0 = none
	offTYC    = 20 // uint32, packed Tycho-2 designation; 0 = none
	offGaia   = 24 // uint64, Gaia DR3 source id; 0 = none
	offMag    = 32 // int16, V magnitude ×100
	offAbsMag = 34 // int16, absolute magnitude ×100; noInt16 = unknown
	offCI     = 36 // int16, B−V colour index ×1000; noInt16 = unknown
	offRV     = 38 // int16, radial velocity ×10 km/s; noInt16 = unknown
	offPMRA   = 40 // int16, proper motion mas/yr (with the cos δ factor)
	offPMDec  = 42 // int16, proper motion mas/yr
	offProper = 44 // uint16, 1-based index into the proper-name table; 0 = none
	offSpect  = 46 // uint16, 1-based index into the spectral-type table; 0 = none
	offFlam   = 48 // uint16, Flamsteed number; 0 = none
	offBayer  = 50 // uint8, greekIndex*10 + component; 0 = none
	offCon    = 51 // uint8, 1-based index into the constellation table; 0 = none
)

// noInt16 marks an absent value in the fields where 0 is itself meaningful (an A0 star really does
// have B−V = 0, and a star really can have zero radial velocity).
const noInt16 = math.MinInt16

// maxPlausibleDistPc rejects the distances a bad or negative parallax produces. The Milky Way is
// ~30 kpc across and the farthest satellite galaxies are ~250 kpc, so anything past 100 kpc in a
// star catalogue is a measurement artefact, not a distance — and a wrong number on the hover card
// is worse than no number.
const maxPlausibleDistPc = 100_000

// tycRegionSpan is the stride used to pack a Tycho-2 designation into one uint32. Tycho-2 numbers
// run region 1…9537, number 1…12121, component 1…3; 13000 leaves headroom above the observed
// maximum while keeping (region·13000 + number)·4 + component inside uint32.
const tycRegionSpan = 13000

// greekTokens is the canonical, ORDER-STABLE list of HYG/ATHYG Bayer tokens: the 1-based index into
// this slice is what the binary catalogue stores, so entries may be APPENDED but never reordered or
// removed without bumping fileVersion.
var greekTokens = [...]string{
	"Alp", "Bet", "Gam", "Del", "Eps", "Zet", "Eta", "The", "Iot", "Kap", "Lam", "Mu",
	"Nu", "Xi", "Omi", "Pi", "Rho", "Sig", "Tau", "Ups", "Phi", "Chi", "Psi", "Ome",
}

// greekLetters is greekTokens rendered, index for index.
var greekLetters = [...]string{
	"α", "β", "γ", "δ", "ε", "ζ", "η", "θ", "ι", "κ", "λ", "μ",
	"ν", "ξ", "ο", "π", "ρ", "σ", "τ", "υ", "φ", "χ", "ψ", "ω",
}

// packTYC encodes "1234-5678-1" into a uint32 (0 when the string is not a Tycho-2 designation).
// Region 0 does not exist in Tycho-2, so 0 unambiguously means "none".
func packTYC(s string) uint32 {
	region, number, comp, ok := splitTYC(s)
	if !ok || region < 1 || region >= tycRegionSpan || number < 0 || number >= tycRegionSpan || comp < 0 || comp > 3 {
		return 0
	}
	return uint32((region*tycRegionSpan+number)*4 + comp)
}

// unpackTYC renders a packed designation back to "1234-5678-1" ("" when absent).
func unpackTYC(p uint32) string {
	if p == 0 {
		return ""
	}
	comp := p & 3
	rest := p >> 2
	return strconv.FormatUint(uint64(rest/tycRegionSpan), 10) + "-" +
		strconv.FormatUint(uint64(rest%tycRegionSpan), 10) + "-" +
		strconv.FormatUint(uint64(comp), 10)
}

func splitTYC(s string) (region, number, comp int, ok bool) {
	var parts [3]int
	n, start := 0, 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '-' {
			continue
		}
		if n == 3 {
			return 0, 0, 0, false
		}
		v, err := strconv.Atoi(s[start:i])
		if err != nil {
			return 0, 0, 0, false
		}
		parts[n] = v
		n++
		start = i + 1
	}
	if n != 3 {
		return 0, 0, 0, false
	}
	return parts[0], parts[1], parts[2], true
}

// packBayer encodes an HYG Bayer token ("Alp", "The-2") as greekIndex*10 + component. 0 means the
// token is absent or outside the greek grammar — the same degrade-to-the-next-tier behaviour the
// runtime label chain has always had.
func packBayer(token string) uint8 {
	if token == "" {
		return 0
	}
	base, comp := token, 0
	for i := 0; i < len(token); i++ {
		if token[i] == '-' {
			v, err := strconv.Atoi(token[i+1:])
			if err != nil || v < 0 || v > 9 {
				return 0
			}
			base, comp = token[:i], v
			break
		}
	}
	for i, tok := range greekTokens {
		if tok == base {
			return uint8((i+1)*10 + comp)
		}
	}
	return 0
}

// unpackBayer renders a packed Bayer designation back to the HYG token form the Star carries.
func unpackBayer(b uint8) string {
	if b == 0 {
		return ""
	}
	idx, comp := int(b)/10, int(b)%10
	if idx < 1 || idx > len(greekTokens) {
		return ""
	}
	if comp == 0 {
		return greekTokens[idx-1]
	}
	return greekTokens[idx-1] + "-" + strconv.Itoa(comp)
}

// header describes one catalogue file.
type header struct {
	count  int
	strOff int64
	recOff int64
}

func (h header) marshal() []byte {
	b := make([]byte, headerSize)
	copy(b, fileMagic)
	binary.LittleEndian.PutUint16(b[8:], fileVersion)
	binary.LittleEndian.PutUint16(b[10:], recordSize)
	binary.LittleEndian.PutUint32(b[12:], uint32(h.count))
	binary.LittleEndian.PutUint64(b[16:], uint64(h.strOff))
	binary.LittleEndian.PutUint64(b[24:], uint64(h.recOff))
	return b
}

// errBadFormat is returned for anything this build cannot read; callers fall back to the embedded
// catalogue rather than failing, so a stale or truncated download degrades instead of breaking.
var errBadFormat = errors.New("deepstars: unrecognised catalogue file")

func parseHeader(b []byte) (header, error) {
	if len(b) < headerSize || string(b[:8]) != fileMagic {
		return header{}, errBadFormat
	}
	if v := binary.LittleEndian.Uint16(b[8:]); v != fileVersion {
		return header{}, fmt.Errorf("%w: version %d (this build reads %d)", errBadFormat, v, fileVersion)
	}
	if rs := binary.LittleEndian.Uint16(b[10:]); rs != recordSize {
		return header{}, fmt.Errorf("%w: record size %d (this build reads %d)", errBadFormat, rs, recordSize)
	}
	return header{
		count:  int(binary.LittleEndian.Uint32(b[12:])),
		strOff: int64(binary.LittleEndian.Uint64(b[16:])),
		recOff: int64(binary.LittleEndian.Uint64(b[24:])),
	}, nil
}

// writeStringTable emits one table: a uint32 count then each entry as uint16 length + bytes.
func writeStringTable(w io.Writer, vals []string) error {
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(vals)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	var l [2]byte
	for _, v := range vals {
		if len(v) > math.MaxUint16 {
			return fmt.Errorf("string table entry too long (%d bytes)", len(v))
		}
		binary.LittleEndian.PutUint16(l[:], uint16(len(v)))
		if _, err := w.Write(l[:]); err != nil {
			return err
		}
		if _, err := io.WriteString(w, v); err != nil {
			return err
		}
	}
	return nil
}

// readStringTable reads one table written by writeStringTable, returning it and the bytes consumed.
func readStringTable(b []byte) ([]string, int, error) {
	if len(b) < 4 {
		return nil, 0, errBadFormat
	}
	n := int(binary.LittleEndian.Uint32(b))
	// A corrupt length must not turn into a multi-GB allocation.
	if n < 0 || n > 1<<24 {
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

// encodeRecord writes one star into a recordSize-byte slice. Callers pass table indices already
// resolved; this function owns the scaling and the sentinels so the decoder can mirror it exactly.
func encodeRecord(b []byte, s Star, properIdx, spectIdx, flam uint16, bayer, con uint8) {
	putI32 := func(off int, v float64) {
		binary.LittleEndian.PutUint32(b[off:], uint32(int32(math.Round(v*1e6))))
	}
	putI16 := func(off int, v int) {
		binary.LittleEndian.PutUint16(b[off:], uint16(int16(clampI16(v))))
	}
	putI32(offRA, s.RADeg)
	putI32(offDec, s.DecDeg)

	dist := float32(0)
	if s.DistPc > 0 && s.DistPc < maxPlausibleDistPc {
		dist = float32(s.DistPc)
	}
	binary.LittleEndian.PutUint32(b[offDist:], math.Float32bits(dist))
	binary.LittleEndian.PutUint32(b[offHD:], uint32(max0(s.HD)))
	binary.LittleEndian.PutUint32(b[offHIP:], uint32(max0(s.HIP)))
	binary.LittleEndian.PutUint32(b[offTYC:], packTYC(s.TYC))
	binary.LittleEndian.PutUint64(b[offGaia:], s.Gaia)

	putI16(offMag, int(math.Round(s.Mag*100)))
	if s.HasAbsMag {
		putI16(offAbsMag, int(math.Round(s.AbsMag*100)))
	} else {
		putI16(offAbsMag, noInt16)
	}
	if s.HasCI {
		putI16(offCI, int(math.Round(s.CI*1000)))
	} else {
		putI16(offCI, noInt16)
	}
	if s.HasRV {
		putI16(offRV, int(math.Round(s.RVKmS*10)))
	} else {
		putI16(offRV, noInt16)
	}
	putI16(offPMRA, int(math.Round(s.PMRA)))
	putI16(offPMDec, int(math.Round(s.PMDec)))

	binary.LittleEndian.PutUint16(b[offProper:], properIdx)
	binary.LittleEndian.PutUint16(b[offSpect:], spectIdx)
	binary.LittleEndian.PutUint16(b[offFlam:], flam)
	b[offBayer] = bayer
	b[offCon] = con
}

// decodeRecord is the exact inverse of encodeRecord; the string tables resolve the indices.
func decodeRecord(b []byte, proper, spect, con []string) Star {
	getI32 := func(off int) float64 {
		return float64(int32(binary.LittleEndian.Uint32(b[off:]))) / 1e6
	}
	getI16 := func(off int) int { return int(int16(binary.LittleEndian.Uint16(b[off:]))) }

	s := Star{
		RADeg:  getI32(offRA),
		DecDeg: getI32(offDec),
		DistPc: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[offDist:]))),
		HD:     int(binary.LittleEndian.Uint32(b[offHD:])),
		HIP:    int(binary.LittleEndian.Uint32(b[offHIP:])),
		TYC:    unpackTYC(binary.LittleEndian.Uint32(b[offTYC:])),
		Gaia:   binary.LittleEndian.Uint64(b[offGaia:]),
		Mag:    float64(getI16(offMag)) / 100,
		PMRA:   float64(getI16(offPMRA)),
		PMDec:  float64(getI16(offPMDec)),
		Bayer:  unpackBayer(b[offBayer]),
		Flam:   int(binary.LittleEndian.Uint16(b[offFlam:])),
	}
	if v := getI16(offAbsMag); v != noInt16 {
		s.AbsMag, s.HasAbsMag = float64(v)/100, true
	}
	if v := getI16(offCI); v != noInt16 {
		s.CI, s.HasCI = float64(v)/1000, true
	}
	if v := getI16(offRV); v != noInt16 {
		s.RVKmS, s.HasRV = float64(v)/10, true
	}
	s.Proper = lookupTable(proper, binary.LittleEndian.Uint16(b[offProper:]))
	s.Spect = lookupTable(spect, binary.LittleEndian.Uint16(b[offSpect:]))
	s.Con = lookupTable(con, uint16(b[offCon]))
	return s
}

// decodeDec reads only the declination of a record — the binary search's hot path.
func decodeDec(b []byte) float64 {
	return float64(int32(binary.LittleEndian.Uint32(b[offDec:]))) / 1e6
}

// lookupTable resolves a 1-based table index; anything out of range degrades to "" rather than
// panicking on a corrupt file.
func lookupTable(t []string, idx uint16) string {
	if idx == 0 || int(idx) > len(t) {
		return ""
	}
	return t[idx-1]
}

func clampI16(v int) int {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return v
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
