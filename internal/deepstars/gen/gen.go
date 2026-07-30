// Package gen builds the slimmed catalogue internal/deepstars embeds. It downloads the HYG
// database (network at generation time ONLY), keeps stars at or brighter than the magnitude
// limit, validates every Bayer token against the runtime greek map's grammar (a new HYG variant
// must fail generation, never silently lose its label) and writes a gzipped, magnitude-sorted
// CSV. Run via `just gen-deepstars-data`; commit the regenerated file.
package gen

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/verove-jordan/astronomy/internal/skymapgen"
)

const (
	DefaultMagLimit = 9.0
	DefaultOutPath  = "internal/deepstars/catalogue/hyg_mag9.csv.gz"

	// minRows guards against a truncated download silently producing a near-empty catalogue.
	minRows = 50_000
)

// Options configure one generation run.
type Options struct {
	URL      string  // HYG CSV source ("" → skymapgen.DefaultHYGURL, the same pin as skymap.json)
	MagLimit float64 // faintest magnitude kept (0 → 9.0)
	OutPath  string  // output .csv.gz ("" → the embedded catalogue path)
}

// bayerToken is the grammar the runtime greek map understands: a 3-letter token plus an optional
// numeric component suffix ("Alp", "The-2").
var bayerToken = regexp.MustCompile(`^(Alp|Bet|Gam|Del|Eps|Zet|Eta|The|Iot|Kap|Lam|Mu|Nu|Xi|Omi|Pi|Rho|Sig|Tau|Ups|Phi|Chi|Psi|Ome)(-[0-9])?$`)

type row struct {
	raDeg, decDeg, mag float64
	proper, bayer, con string
	flam, hd           int
	pmra, pmdec        float64
}

// Generate downloads, slims and writes the catalogue.
func Generate(ctx context.Context, o Options) error {
	if o.URL == "" {
		o.URL = skymapgen.DefaultHYGURL
	}
	if o.MagLimit == 0 {
		o.MagLimit = DefaultMagLimit
	}
	if o.OutPath == "" {
		o.OutPath = DefaultOutPath
	}

	body, err := fetch(ctx, o.URL)
	if err != nil {
		return fmt.Errorf("deepstars-data: %w", err)
	}
	defer body.Close()

	rows, err := parse(body, o.MagLimit)
	if err != nil {
		return fmt.Errorf("deepstars-data: %w", err)
	}
	if len(rows) < minRows {
		return fmt.Errorf("deepstars-data: only %d rows at mag ≤ %.1f — source looks truncated", len(rows), o.MagLimit)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].mag < rows[j].mag })

	if err := write(o.OutPath, rows); err != nil {
		return fmt.Errorf("deepstars-data: %w", err)
	}
	fmt.Printf("deepstars-data: wrote %d stars (mag ≤ %.1f) to %s\n", len(rows), o.MagLimit, o.OutPath)
	return nil
}

func fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// parse reads the HYG CSV (header-driven, robust to column reordering) and keeps rows at or below
// magLimit. HYG stores RA in hours (×15 → degrees).
func parse(r io.Reader, magLimit float64) ([]row, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"ra", "dec", "mag", "proper"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("source is missing the %q column", need)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}

	var rows []row
	for {
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("read row: %w", e)
		}
		proper := get(rec, "proper")
		if proper == "Sol" { // the Sun sits at the catalogue origin
			continue
		}
		raHours, e1 := strconv.ParseFloat(get(rec, "ra"), 64)
		dec, e2 := strconv.ParseFloat(get(rec, "dec"), 64)
		mag, e3 := strconv.ParseFloat(get(rec, "mag"), 64)
		if e1 != nil || e2 != nil || e3 != nil || mag > magLimit {
			continue
		}
		bayer := get(rec, "bayer")
		if bayer != "" && !bayerToken.MatchString(bayer) {
			return nil, fmt.Errorf("unknown Bayer token %q (row %v) — extend the runtime greek map first", bayer, rec)
		}
		x := row{
			raDeg: raHours * 15, decDeg: dec, mag: mag,
			proper: proper, bayer: bayer, con: get(rec, "con"),
		}
		x.flam, _ = strconv.Atoi(get(rec, "flam"))
		x.hd, _ = strconv.Atoi(get(rec, "hd"))
		if v, err := strconv.ParseFloat(get(rec, "pmra"), 64); err == nil {
			x.pmra = v
		}
		if v, err := strconv.ParseFloat(get(rec, "pmdec"), 64); err == nil {
			x.pmdec = v
		}
		rows = append(rows, x)
	}
	return rows, nil
}

// write emits the gzipped CSV atomically (temp + rename).
func write(outPath string, rows []row) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".deepstars-*.csv.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	gz, _ := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	cw := csv.NewWriter(gz)
	if err := cw.Write([]string{"ra_deg", "dec_deg", "mag", "proper", "bayer", "flam", "con", "hd", "pmra", "pmdec"}); err != nil {
		return err
	}
	for _, x := range rows {
		rec := []string{
			strconv.FormatFloat(round4(x.raDeg), 'f', -1, 64),
			strconv.FormatFloat(round4(x.decDeg), 'f', -1, 64),
			strconv.FormatFloat(math.Round(x.mag*100)/100, 'f', -1, 64),
			x.proper,
			x.bayer,
			intOrEmpty(x.flam),
			x.con,
			intOrEmpty(x.hd),
			strconv.Itoa(int(math.Round(x.pmra))),
			strconv.Itoa(int(math.Round(x.pmdec))),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp files are 0600; this one is a committed asset.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), outPath)
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func intOrEmpty(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}
