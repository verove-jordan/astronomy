package deepstars

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"
)

// Catalog is one star catalogue a caller can query. It is either the embedded HYG extract (bright
// stars only, always available, fully in memory) or the downloaded ATHYG file (2.5 million stars,
// read from disk a declination band at a time). Both answer the same InField question, so callers
// never branch on which one they got — only Deep() reports it, for logging and for the UI.
type Catalog struct {
	// f is nil for the embedded catalogue; non-nil for the deep on-disk one.
	f      *os.File
	hdr    header
	proper []string
	spect  []string
	con    []string

	// stars backs the embedded catalogue (magnitude-ascending).
	stars []Star
}

// deepUncappedMax bounds an InField call that asks for "everything" on the DEEP catalogue. A
// whole-sky query would otherwise materialise 2.5 million Stars (~600 MB); callers that mean it
// should pass an explicit maxN. Every in-repo caller does.
const deepUncappedMax = 200_000

// scanChunk is how many records a band scan reads per syscall (~210 KB).
const scanChunk = 4096

// Open reads the deep catalogue at path. It validates the header eagerly and loads the (small)
// string tables, keeping the file open for band reads; Close releases it.
func Open(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	c, err := openFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("deepstars: %s: %w", path, err)
	}
	return c, nil
}

func openFile(f *os.File) (*Catalog, error) {
	hb := make([]byte, headerSize)
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, headerSize), hb); err != nil {
		return nil, errBadFormat
	}
	hdr, err := parseHeader(hb)
	if err != nil {
		return nil, err
	}
	if hdr.count <= 0 || hdr.recOff < hdr.strOff || hdr.strOff < headerSize {
		return nil, errBadFormat
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if want := hdr.recOff + int64(hdr.count)*recordSize; st.Size() < want {
		return nil, fmt.Errorf("%w: truncated (%d bytes, expected %d)", errBadFormat, st.Size(), want)
	}

	tables := make([]byte, hdr.recOff-hdr.strOff)
	if _, err := io.ReadFull(io.NewSectionReader(f, hdr.strOff, int64(len(tables))), tables); err != nil {
		return nil, errBadFormat
	}
	c := &Catalog{f: f, hdr: hdr}
	off := 0
	for _, dst := range []*[]string{&c.proper, &c.spect, &c.con} {
		vals, n, err := readStringTable(tables[off:])
		if err != nil {
			return nil, err
		}
		*dst = vals
		off += n
	}
	return c, nil
}

// Load returns the catalogue at path, falling back to the embedded HYG extract when path is empty,
// missing or unreadable. It never fails: an absent download means shallower names, not a broken
// feature, so every caller can treat the result as usable. The boolean reports whether the deep
// catalogue was actually opened, which is worth logging once per run.
func Load(path string) (*Catalog, bool) {
	if path != "" {
		if c, err := Open(path); err == nil {
			return c, true
		}
	}
	stars, err := load()
	if err != nil {
		stars = nil
	}
	return &Catalog{stars: stars}, false
}

// Close releases the file handle (a no-op for the embedded catalogue).
func (c *Catalog) Close() error {
	if c == nil || c.f == nil {
		return nil
	}
	return c.f.Close()
}

// Deep reports whether this is the downloaded ATHYG catalogue rather than the embedded extract.
func (c *Catalog) Deep() bool { return c != nil && c.f != nil }

// Count returns the number of stars in the catalogue.
func (c *Catalog) Count() int {
	if c == nil {
		return 0
	}
	if c.f != nil {
		return c.hdr.count
	}
	return len(c.stars)
}

// InField returns the catalogue stars within radiusDeg of the field center at the given epoch,
// magnitude-ascending, capped at maxN. Positions are advanced by proper motion from J2000 to epoch;
// RA wrap and pole fields are handled by unit-vector math.
//
// maxN ≤ 0 means "all of them" for the embedded catalogue, and the deepUncappedMax brightest for
// the deep one — 2.5 million Star values cannot be materialised, so a caller that wants a specific
// depth must say so.
func (c *Catalog) InField(centerRADeg, centerDecDeg, radiusDeg float64, maxN int, epoch time.Time) []Star {
	if c == nil {
		return nil
	}
	if c.f == nil {
		return inFieldSorted(c.stars, centerRADeg, centerDecDeg, radiusDeg, maxN, epoch)
	}
	if maxN <= 0 {
		maxN = deepUncappedMax
	}
	return c.inFieldDeep(centerRADeg, centerDecDeg, radiusDeg, maxN, epoch)
}

// cone is the reusable "is this star inside the field?" test: one set of trig for the field center,
// then a dot product per star. Shared by both catalogue backings so they can never disagree about
// what "in field" means.
type cone struct {
	centerRA     float64
	sinD0, cosD0 float64
	cosR         float64
	decLo, decHi float64
	years        float64
}

func newCone(centerRADeg, centerDecDeg, radiusDeg float64, epoch time.Time) cone {
	const degRad = math.Pi / 180
	sinD0, cosD0 := math.Sincos(centerDecDeg * degRad)
	return cone{
		centerRA: centerRADeg,
		sinD0:    sinD0, cosD0: cosD0,
		cosR:  math.Cos(radiusDeg * degRad),
		decLo: centerDecDeg - radiusDeg - pmMarginDeg,
		decHi: centerDecDeg + radiusDeg + pmMarginDeg,
		years: epoch.Sub(j2000).Hours() / 24 / 365.25,
	}
}

// admit advances s to the epoch in place and reports whether it lands inside the cone.
func (k cone) admit(s *Star) bool {
	const degRad = math.Pi / 180
	if s.DecDeg < k.decLo || s.DecDeg > k.decHi {
		return false
	}
	s.DecDeg += s.PMDec / 3.6e6 * k.years
	if c := math.Cos(s.DecDeg * degRad); c > 1e-6 {
		s.RADeg += s.PMRA / 3.6e6 * k.years / c
	}
	sinD, cosD := math.Sincos(s.DecDeg * degRad)
	return sinD*k.sinD0+cosD*k.cosD0*math.Cos((s.RADeg-k.centerRA)*degRad) >= k.cosR
}

// inFieldSorted filters an in-memory, magnitude-ascending catalogue. Because the source is already
// sorted it can stop at maxN — the behaviour the embedded catalogue has always had.
func inFieldSorted(stars []Star, centerRADeg, centerDecDeg, radiusDeg float64, maxN int, epoch time.Time) []Star {
	k := newCone(centerRADeg, centerDecDeg, radiusDeg, epoch)
	var out []Star
	for _, s := range stars {
		if !k.admit(&s) {
			continue
		}
		out = append(out, s)
		if maxN > 0 && len(out) == maxN {
			break
		}
	}
	return out
}

// inFieldDeep answers a cone query against the on-disk catalogue: binary-search the declination
// band, stream it, and keep the maxN brightest hits.
func (c *Catalog) inFieldDeep(centerRADeg, centerDecDeg, radiusDeg float64, maxN int, epoch time.Time) []Star {
	k := newCone(centerRADeg, centerDecDeg, radiusDeg, epoch)
	lo := c.searchDec(k.decLo)
	if lo >= c.hdr.count {
		return nil
	}

	// keep holds hits with a magnitude cut that tightens as the buffer fills: once there are 2·maxN
	// candidates it is sorted, trimmed to maxN, and everything fainter than the new worst survivor is
	// skipped outright. Memory stays bounded at 2·maxN however wide the band is.
	keep := make([]Star, 0, min(2*maxN, 4096))
	cut := math.Inf(1)
	trim := func() {
		sortByMag(keep)
		keep = keep[:maxN]
		cut = keep[len(keep)-1].Mag
	}

	buf := make([]byte, scanChunk*recordSize)
	for i := lo; i < c.hdr.count; {
		n := min(scanChunk, c.hdr.count-i)
		got, err := c.f.ReadAt(buf[:n*recordSize], c.hdr.recOff+int64(i)*recordSize)
		n = got / recordSize
		if n == 0 {
			break
		}
		done := false
		for j := 0; j < n; j++ {
			rec := buf[j*recordSize : (j+1)*recordSize]
			// Records are dec-sorted, so the first star past the band ends the whole scan.
			if decodeDec(rec) > k.decHi {
				done = true
				break
			}
			if float64(int16(binary.LittleEndian.Uint16(rec[offMag:])))/100 > cut {
				continue
			}
			s := decodeRecord(rec, c.proper, c.spect, c.con)
			if !k.admit(&s) {
				continue
			}
			keep = append(keep, s)
			if len(keep) >= 2*maxN {
				trim()
			}
		}
		if done || err != nil {
			break
		}
		i += n
	}

	sortByMag(keep)
	if len(keep) > maxN {
		keep = keep[:maxN]
	}
	return keep
}

// searchDec returns the index of the first record whose declination is ≥ dec, by binary search over
// the fixed-width records (about 22 four-byte reads for a 2.5-million-star file).
func (c *Catalog) searchDec(dec float64) int {
	var b [4]byte
	return sort.Search(c.hdr.count, func(i int) bool {
		off := c.hdr.recOff + int64(i)*recordSize + offDec
		if _, err := c.f.ReadAt(b[:], off); err != nil {
			return true // an unreadable record ends the search rather than looping
		}
		return float64(int32(binary.LittleEndian.Uint32(b[:])))/1e6 >= dec
	})
}

func sortByMag(s []Star) {
	sort.Slice(s, func(i, j int) bool { return s[i].Mag < s[j].Mag })
}
