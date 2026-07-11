package lightpollution

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Coverage describes the offline atlas currently installed (or that was just built) for the UI's
// "offline light-pollution data" panel. Present=false means no atlas is loaded (the app runs on GIBS).
type Coverage struct {
	Present   bool    `json:"present"`
	MinLat    float64 `json:"min_lat"`
	MinLon    float64 `json:"min_lon"`
	MaxLat    float64 `json:"max_lat"`
	MaxLon    float64 `json:"max_lon"`
	Rows      int     `json:"rows"`
	Cols      int     `json:"cols"`
	Unit      string  `json:"unit"`
	BuiltAtMs int64   `json:"built_at_ms"`
}

// ResolveBounds turns a request into a bounding box: an explicit "minLat,minLon,maxLat,maxLon" bbox wins;
// otherwise the named region preset ("" → "france"). Unknown regions are an error.
func ResolveBounds(region, bbox string) (Bounds, error) {
	if strings.TrimSpace(bbox) != "" {
		return parseBBox(bbox)
	}
	if region == "" {
		region = "france"
	}
	b, ok := RegionBounds[strings.ToLower(region)]
	if !ok {
		return Bounds{}, fmt.Errorf("unknown region %q (want: france, europe, world, or --bbox)", region)
	}
	return b, nil
}

func parseBBox(s string) (Bounds, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return Bounds{}, fmt.Errorf("bbox must be minLat,minLon,maxLat,maxLon")
	}
	v := make([]float64, 4)
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return Bounds{}, fmt.Errorf("bbox value %q: %w", p, err)
		}
		v[i] = f
	}
	b := Bounds{MinLat: v[0], MinLon: v[1], MaxLat: v[2], MaxLon: v[3]}
	if b.MaxLat <= b.MinLat || b.MaxLon <= b.MinLon {
		return Bounds{}, fmt.Errorf("bbox max must exceed min: %+v", b)
	}
	return b, nil
}

// TileCount returns how many 5° tiles cover b — used to size a progress bar and to guard huge requests.
func TileCount(b Bounds) int {
	txMin, txMax, tyMin, tyMax, ok := djTileRange(b)
	if !ok {
		return 0
	}
	return (txMax - txMin + 1) * (tyMax - tyMin + 1)
}

// BuildAtlas downloads the djlorenz tiles covering b for the given year (0 → latest default) and writes
// atlas.bin/atlas.json into dir. onProgress (optional) is called after each tile with (done, total). It is
// the shared entry point for both the `astrostack lightpollution-atlas` CLI and the in-app build endpoint.
func BuildAtlas(ctx context.Context, dir string, b Bounds, year int, client *http.Client, onProgress func(done, total int)) (Coverage, error) {
	if year <= 0 {
		year = djDefaultYear
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	total := TileCount(b)
	if total == 0 {
		return Coverage{}, fmt.Errorf("bounds select no djlorenz tiles: %+v", b)
	}

	done := 0
	fetch := func(tx, ty int) ([]byte, error) {
		body, err := fetchTile(ctx, client, year, tx, ty)
		done++
		if onProgress != nil {
			onProgress(done, total)
		}
		return body, err
	}

	cells, meta, err := buildAtlasGrid(b, fetch)
	if err != nil {
		return Coverage{}, err
	}
	if err := writeAtlasFiles(dir, meta, cells); err != nil {
		return Coverage{}, err
	}
	return coverageFromMeta(meta, dir), nil
}

// fetchTile GETs one gzipped tile with a couple of retries. A 404 (djlorenz omits some all-ocean tiles) is
// returned as an error without retry, so the caller leaves those cells as nodata.
func fetchTile(ctx context.Context, client *http.Client, year, tx, ty int) ([]byte, error) {
	url := DjlorenzTileURL(year, tx, ty)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("tile %d_%d absent (HTTP 404)", tx, ty)
		case rerr != nil:
			lastErr = fmt.Errorf("read tile %d_%d: %w", tx, ty, rerr)
		default:
			lastErr = fmt.Errorf("tile %d_%d: HTTP %d", tx, ty, resp.StatusCode)
		}
	}
	return nil, lastErr
}

func coverageFromMeta(m atlasMeta, dir string) Coverage {
	cov := Coverage{
		Present: true,
		MinLat:  m.LatMin, MinLon: m.LonMin, MaxLat: m.LatMax, MaxLon: m.LonMax,
		Rows: m.Rows, Cols: m.Cols, Unit: m.Unit,
	}
	if fi, err := os.Stat(filepath.Join(dir, "atlas.bin")); err == nil {
		cov.BuiltAtMs = fi.ModTime().UnixMilli()
	}
	return cov
}
