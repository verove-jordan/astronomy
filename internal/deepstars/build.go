package deepstars

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Build converts the ATHYG source CSVs into the dec-sorted binary catalogue the engine queries.
// Network (or disk) is touched at BUILD time only — the engine itself never downloads anything.
//
// Run it through `just download-deepstars`; see catalogue/README.md for provenance and licence.

// DefaultATHYGURLs are the two halves of ATHYG v3.2, pinned to the same publisher (astronexus) and
// URL shape as the HYG file skymapgen and the embedded extract already use. The second half carries
// NO header row — the release splits one CSV by byte count, not by document — so the builder
// applies the first file's header to both.
var DefaultATHYGURLs = []string{
	"https://raw.githubusercontent.com/astronexus/ATHYG-Database/main/data/athyg_v32-1.csv.gz",
	"https://raw.githubusercontent.com/astronexus/ATHYG-Database/main/data/athyg_v32-2.csv.gz",
}

// DefaultCatalogFile is the file name the engine looks for inside <library>/catalogues.
const DefaultCatalogFile = "athyg_v32.bin"

// minBuildRows guards against a truncated or redirected download quietly producing a catalogue that
// is worse than the embedded one it is meant to replace. A var, not a const, so format tests can
// build a two-star fixture through the production encoder.
var minBuildRows = 1_000_000

// BuildOptions configure one conversion.
type BuildOptions struct {
	// Sources are gzipped ATHYG CSVs, as http(s) URLs or local paths, in order. Empty →
	// DefaultATHYGURLs.
	Sources []string
	// OutPath is the .bin to write (atomically, temp + rename).
	OutPath string
	// MagLimit drops stars fainter than this; 0 keeps every row. The default keeps everything: the
	// whole point of the deep catalogue is the 11th-magnitude field stars a stack actually resolves.
	MagLimit float64
	// Log receives progress lines; nil discards them.
	Log func(string)
}

// athygHeader is the v3.2 column order. It is applied to the header-less second file, and verified
// against the first file's real header so a schema change fails the build instead of silently
// shifting every column.
var athygHeader = strings.Split(
	"id,tyc,gaia,hyg,hip,hd,hr,gl,bayer,flam,con,proper,ra,dec,pos_src,dist,x0,y0,z0,dist_src,"+
		"mag,absmag,ci,mag_src,rv,rv_src,pm_ra,pm_dec,pm_src,vx,vy,vz,spect,spect_src", ",")

// interner assigns stable 1-based table indices to repeated strings (proper names, spectral types,
// constellations) so 2.5 million records can carry a 2-byte index instead of a string.
type interner struct {
	idx  map[string]int
	vals []string
	max  int // largest index the record field can hold
	what string
}

func newInterner(what string, max int) *interner {
	return &interner{idx: map[string]int{}, max: max, what: what}
}

func (n *interner) get(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	if i, ok := n.idx[s]; ok {
		return i, nil
	}
	if len(n.vals) >= n.max {
		return 0, fmt.Errorf("more than %d distinct %s values — widen the record field", n.max, n.what)
	}
	n.vals = append(n.vals, s)
	i := len(n.vals)
	n.idx[s] = i
	return i, nil
}

// sortKey pairs a record's declination with its position in the staging buffer, so the sort moves
// 8 bytes per star instead of a 52-byte record.
type sortKey struct {
	dec int32
	rec int32
}

// Build reads the sources, encodes every star and writes the dec-sorted catalogue.
func Build(ctx context.Context, o BuildOptions) error {
	if len(o.Sources) == 0 {
		o.Sources = DefaultATHYGURLs
	}
	if o.OutPath == "" {
		return fmt.Errorf("deepstars: build needs an output path")
	}
	logf := func(format string, a ...any) {
		if o.Log != nil {
			o.Log(fmt.Sprintf(format, a...))
		}
	}

	proper := newInterner("proper name", math.MaxUint16)
	spect := newInterner("spectral type", math.MaxUint16)
	con := newInterner("constellation", math.MaxUint8)

	// recs stages every encoded record back to back; keys is what actually gets sorted.
	recs := make([]byte, 0, 64<<20)
	keys := make([]sortKey, 0, 1<<21)
	var rec [recordSize]byte
	skipped := 0

	for i, src := range o.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		logf("reading %s", src)
		rc, err := openSource(ctx, src)
		if err != nil {
			return fmt.Errorf("deepstars: %w", err)
		}
		n, skip, err := readATHYG(rc, i == 0, o.MagLimit, func(s Star) error {
			pi, err := proper.get(s.Proper)
			if err != nil {
				return err
			}
			si, err := spect.get(s.Spect)
			if err != nil {
				return err
			}
			ci, err := con.get(s.Con)
			if err != nil {
				return err
			}
			bayer := packBayer(s.Bayer)
			if s.Bayer != "" && bayer == 0 {
				return fmt.Errorf("unknown Bayer token %q — extend greekTokens first", s.Bayer)
			}
			if s.Flam < 0 || s.Flam > math.MaxUint16 {
				return fmt.Errorf("Flamsteed number %d out of range", s.Flam)
			}
			encodeRecord(rec[:], s, uint16(pi), uint16(si), uint16(s.Flam), bayer, uint8(ci))
			keys = append(keys, sortKey{dec: int32(decMicro(s.DecDeg)), rec: int32(len(keys))})
			recs = append(recs, rec[:]...)
			return nil
		})
		rc.Close()
		if err != nil {
			return fmt.Errorf("deepstars: %s: %w", src, err)
		}
		skipped += skip
		logf("  %d stars (%d rows skipped)", n, skip)
	}

	if len(keys) < minBuildRows {
		return fmt.Errorf("deepstars: only %d stars — the source looks truncated", len(keys))
	}
	logf("sorting %d stars by declination", len(keys))
	sort.Slice(keys, func(i, j int) bool { return keys[i].dec < keys[j].dec })

	logf("writing %s", o.OutPath)
	if err := writeCatalog(o.OutPath, recs, keys, proper.vals, spect.vals, con.vals); err != nil {
		return fmt.Errorf("deepstars: %w", err)
	}
	logf("done: %d stars, %d proper names, %d spectral types, %d constellations, %d rows skipped",
		len(keys), len(proper.vals), len(spect.vals), len(con.vals), skipped)
	return nil
}

// openSource resolves an http(s) URL or a local path to a gunzipped reader.
func openSource(ctx context.Context, src string) (io.ReadCloser, error) {
	var raw io.ReadCloser
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, err
		}
		resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("GET %s: status %d", src, resp.StatusCode)
		}
		raw = resp.Body
	} else {
		f, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		raw = f
	}
	gz, err := gzip.NewReader(raw)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("gunzip %s: %w", src, err)
	}
	return gzReadCloser{gz, raw}, nil
}

type gzReadCloser struct {
	*gzip.Reader
	under io.Closer
}

func (g gzReadCloser) Close() error {
	err := g.Reader.Close()
	if e := g.under.Close(); err == nil {
		err = e
	}
	return err
}

// readATHYG streams one ATHYG CSV, calling emit for every usable star. hasHeader is false for the
// continuation file, whose first line is already data.
func readATHYG(r io.Reader, hasHeader bool, magLimit float64, emit func(Star) error) (kept, skipped int, err error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	cr.FieldsPerRecord = len(athygHeader)

	if hasHeader {
		got, err := cr.Read()
		if err != nil {
			return 0, 0, fmt.Errorf("read header: %w", err)
		}
		if len(got) != len(athygHeader) {
			return 0, 0, fmt.Errorf("header has %d columns, expected %d", len(got), len(athygHeader))
		}
		for i, want := range athygHeader {
			if got[i] != want {
				return 0, 0, fmt.Errorf("column %d is %q, expected %q — the ATHYG schema changed", i, got[i], want)
			}
		}
	}

	col := map[string]int{}
	for i, h := range athygHeader {
		col[h] = i
	}
	for {
		row, e := cr.Read()
		if e == io.EOF {
			return kept, skipped, nil
		}
		if e != nil {
			return kept, skipped, fmt.Errorf("read row: %w", e)
		}
		s, ok := starFromRow(row, col, magLimit)
		if !ok {
			skipped++
			continue
		}
		if err := emit(s); err != nil {
			return kept, skipped, err
		}
		kept++
	}
}

// starFromRow maps one ATHYG row to a Star. ATHYG stores RA in HOURS (×15 → degrees) — the same
// convention as HYG, and the single easiest thing to get wrong here.
func starFromRow(row []string, col map[string]int, magLimit float64) (Star, bool) {
	get := func(name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return row[i]
		}
		return ""
	}
	if get("proper") == "Sol" { // the Sun sits at the catalogue origin, not on the sky
		return Star{}, false
	}
	raHours, e1 := strconv.ParseFloat(get("ra"), 64)
	dec, e2 := strconv.ParseFloat(get("dec"), 64)
	mag, e3 := strconv.ParseFloat(get("mag"), 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return Star{}, false
	}
	if magLimit > 0 && mag > magLimit {
		return Star{}, false
	}
	ra := raHours * 15
	if ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
		return Star{}, false
	}

	s := Star{
		RADeg: ra, DecDeg: dec, Mag: mag,
		Proper: get("proper"),
		Bayer:  get("bayer"),
		Con:    get("con"),
		TYC:    get("tyc"),
		Spect:  strings.TrimSpace(get("spect")),
	}
	s.Flam, _ = strconv.Atoi(get("flam"))
	s.HD, _ = strconv.Atoi(get("hd"))
	s.HIP, _ = strconv.Atoi(get("hip"))
	s.Gaia, _ = strconv.ParseUint(get("gaia"), 10, 64)
	s.PMRA, _ = strconv.ParseFloat(get("pm_ra"), 64)
	s.PMDec, _ = strconv.ParseFloat(get("pm_dec"), 64)
	if v, err := strconv.ParseFloat(get("dist"), 64); err == nil && v > 0 && v < maxPlausibleDistPc {
		s.DistPc = v
	}
	if v, err := strconv.ParseFloat(get("absmag"), 64); err == nil {
		s.AbsMag, s.HasAbsMag = v, true
	}
	if v, err := strconv.ParseFloat(get("ci"), 64); err == nil {
		s.CI, s.HasCI = v, true
	}
	if v, err := strconv.ParseFloat(get("rv"), 64); err == nil {
		s.RVKmS, s.HasRV = v, true
	}
	return s, true
}

func decMicro(dec float64) int32 { return int32(math.Round(dec * 1e6)) }

// writeCatalog emits header + string tables + dec-ordered records, atomically (temp + rename) so a
// reader never sees a half-written catalogue and an interrupted build leaves the old one in place.
func writeCatalog(outPath string, recs []byte, keys []sortKey, proper, spect, con []string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".athyg-*.bin")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	// The string tables sit between the header and the records, so their size has to be known before
	// anything is written: build them in memory first (they are a few hundred KB).
	var tables bytes.Buffer
	for _, t := range [][]string{proper, spect, con} {
		if err := writeStringTable(&tables, t); err != nil {
			tmp.Close()
			return err
		}
	}
	hdr := header{
		count:  len(keys),
		strOff: headerSize,
		recOff: headerSize + int64(tables.Len()),
	}

	w := bufio.NewWriterSize(tmp, 1<<20)
	if _, err := w.Write(hdr.marshal()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := w.Write(tables.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	for _, k := range keys {
		off := int(k.rec) * recordSize
		if _, err := w.Write(recs[off : off+recordSize]); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), outPath)
}
