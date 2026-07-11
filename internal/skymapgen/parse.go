package skymapgen

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rawStar is one catalogue star with the fields the sky map needs.
type rawStar struct {
	HIP    int
	RADeg  float64
	DecDeg float64
	Mag    float64
	Name   string // proper name only ("Vega"); empty for the anonymous background field
}

// segment is one constellation-figure line between two stars, by HIP number.
type segment struct {
	A, B int
	Con  string
}

func httpGet(ctx context.Context, url string) (io.ReadCloser, error) {
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

// fetchStars downloads the HYG catalogue and returns (a) the stars at or below magLimit and (b) an
// index of EVERY star by HIP (including fainter ones), so constellation lines can pull in the few faint
// endpoint stars they need. HYG stores RA in hours (×15 → degrees).
func fetchStars(ctx context.Context, url string, magLimit float64) (kept []rawStar, byHip map[int]rawStar, err error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	defer body.Close()

	r := csv.NewReader(body)
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	col := columnIndex(header)

	byHip = map[int]rawStar{}
	for {
		rec, e := r.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, nil, fmt.Errorf("read row: %w", e)
		}
		s, ok := parseStarRow(rec, col)
		if !ok {
			continue
		}
		if s.HIP > 0 {
			byHip[s.HIP] = s
		}
		if s.Mag <= magLimit {
			kept = append(kept, s)
		}
	}
	return kept, byHip, nil
}

// columnIndex maps HYG header names to positions, so the parser is robust to column reordering.
func columnIndex(header []string) map[string]int {
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.Trim(h, `"`))] = i
	}
	return col
}

func parseStarRow(rec []string, col map[string]int) (rawStar, bool) {
	get := func(name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
	proper := get("proper")
	if proper == "Sol" { // the Sun sits at the catalogue origin — never a sky-map star
		return rawStar{}, false
	}
	raHours, e1 := strconv.ParseFloat(get("ra"), 64)
	dec, e2 := strconv.ParseFloat(get("dec"), 64)
	mag, e3 := strconv.ParseFloat(get("mag"), 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return rawStar{}, false
	}
	hip := 0
	if v, err := strconv.Atoi(get("hip")); err == nil {
		hip = v
	}
	return rawStar{HIP: hip, RADeg: raHours * 15, DecDeg: dec, Mag: mag, Name: proper}, true
}

// fetchConstellationLines parses Stellarium's `.fab`: each line is `ABBR N h1 h2 h3 h4 …` — N segments
// followed by 2N HIP numbers, consecutive pairs forming the figure's lines.
func fetchConstellationLines(ctx context.Context, url string) ([]segment, error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var out []segment
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		abbr := fields[0]
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		hips := fields[2:]
		if len(hips) < 2*n {
			continue
		}
		for k := 0; k < n; k++ {
			a, e1 := strconv.Atoi(hips[2*k])
			b, e2 := strconv.Atoi(hips[2*k+1])
			if e1 == nil && e2 == nil {
				out = append(out, segment{A: a, B: b, Con: abbr})
			}
		}
	}
	return out, sc.Err()
}

var engNameRe = regexp.MustCompile(`_\("([^"]*)"\)`)

// fetchConstellationNames parses `constellation_names.eng.fab`: `ABBR "native" _("English")`.
func fetchConstellationNames(ctx context.Context, url string) (map[string]string, error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	names := map[string]string{}
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		abbr := strings.Fields(line)[0]
		if m := engNameRe.FindStringSubmatch(line); len(m) == 2 {
			names[abbr] = m[1]
		}
	}
	return names, sc.Err()
}
