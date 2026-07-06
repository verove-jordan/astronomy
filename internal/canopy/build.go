package canopy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/geogrid"
)

// ErrNoGDAL is returned when a canopy download is requested but gdal is not installed. Reading the ETH
// canopy COGs needs gdal (there is no pure-Go COG reader here), so the in-app build depends on it — unlike
// the light-pollution atlas, which is pure Go. The horizon still soft-falls to terrain-only without canopy.
var ErrNoGDAL = errors.New("gdal not found — install it (e.g. `brew install gdal`) to download canopy data")

// maxBuildTiles caps how many 3° source tiles one build may span, to guard against an accidental
// continent-sized request (each tile is a several-hundred-MB COG). A drawn area or France fits well under.
const maxBuildTiles = 40

// Bounds is a lat/lon bounding box for a canopy-atlas build.
type Bounds struct {
	MinLat, MinLon, MaxLat, MaxLon float64
}

// RegionBounds are the download-panel presets. Canopy atlases are regional (a whole-world 10 m atlas is
// infeasible); "custom" (the drawn search area) is the recommended, smallest option.
var RegionBounds = map[string]Bounds{
	"france": {MinLat: 41, MinLon: -5, MaxLat: 51.5, MaxLon: 10},
}

// Coverage describes the canopy atlas currently installed (or just built), for the UI's download panel.
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
// otherwise the named region preset ("" → "france").
func ResolveBounds(region, bbox string) (Bounds, error) {
	if strings.TrimSpace(bbox) != "" {
		return parseBBox(bbox)
	}
	if region == "" {
		region = "france"
	}
	b, ok := RegionBounds[strings.ToLower(region)]
	if !ok {
		return Bounds{}, fmt.Errorf("unknown region %q (want: france, or a bbox / drawn area)", region)
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

// TileCount returns how many 3° ETH tiles cover b — used to size progress and guard huge requests.
func TileCount(b Bounds) int { return len(sourceTiles(b, "")) }

// ethTileName is the ETH 3°-grid SW-corner token: latitude N/S 2-digit, longitude E/W 3-digit
// (e.g. lat 45, lon 3 → "N45E003"; lat -3, lon -72 → "S03W072").
func ethTileName(latSW, lonSW int) string {
	latP, lat := "N", latSW
	if latSW < 0 {
		latP, lat = "S", -latSW
	}
	lonP, lon := "E", lonSW
	if lonSW < 0 {
		lonP, lon = "W", -lonSW
	}
	return fmt.Sprintf("%s%02d%s%03d", latP, lat, lonP, lon)
}

// sourceTiles returns the source URLs of every 3° ETH tile intersecting b, expanding {tile} in urlTmpl.
// A blank urlTmpl yields the bare tokens (used by TileCount).
func sourceTiles(b Bounds, urlTmpl string) []string {
	floor3 := func(v float64) int { return int(math.Floor(v/3)) * 3 }
	var out []string
	for lat := floor3(b.MinLat); float64(lat) < b.MaxLat; lat += 3 {
		for lon := floor3(b.MinLon); float64(lon) < b.MaxLon; lon += 3 {
			token := ethTileName(lat, lon)
			if urlTmpl == "" {
				out = append(out, token)
			} else {
				out = append(out, strings.ReplaceAll(urlTmpl, "{tile}", token))
			}
		}
	}
	return out
}

// BuildAtlas downloads the canopy-height data covering b and writes the geogrid atlas to binPath (+ its JSON
// sidecar). It drives gdal over the ETH COGs via /vsicurl/, so only the windows/overviews for b at resDeg
// are fetched (not the full multi-hundred-MB tiles). onProgress (optional) reports coarse step counts.
func (p *Provider) BuildAtlas(ctx context.Context, binPath string, b Bounds, resDeg float64, onProgress func(done, total int)) (Coverage, error) {
	if _, err := exec.LookPath("gdalwarp"); err != nil {
		return Coverage{}, ErrNoGDAL
	}
	if _, err := exec.LookPath("gdalbuildvrt"); err != nil {
		return Coverage{}, ErrNoGDAL
	}
	tiles := sourceTiles(b, p.sourceURL)
	if len(tiles) == 0 {
		return Coverage{}, fmt.Errorf("bounds select no canopy tiles: %+v", b)
	}
	if len(tiles) > maxBuildTiles {
		return Coverage{}, fmt.Errorf("area too large: %d source tiles (max %d) — draw a smaller area", len(tiles), maxBuildTiles)
	}
	if resDeg <= 0 {
		resDeg = 0.0008
	}
	total := len(tiles) + 1 // one step per tile (VRT references) + the warp
	if onProgress != nil {
		onProgress(0, total)
	}

	work, err := os.MkdirTemp("", "canopybuild-")
	if err != nil {
		return Coverage{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()

	// A file list of /vsicurl/ URLs → one VRT mosaic (no data downloaded yet; COGs are opened lazily).
	var list strings.Builder
	for _, u := range tiles {
		list.WriteString("/vsicurl/" + u + "\n")
	}
	listPath := filepath.Join(work, "tiles.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return Coverage{}, err
	}
	vrt := filepath.Join(work, "mosaic.vrt")
	if out, err := runGDAL(ctx, "gdalbuildvrt", "-input_file_list", listPath, vrt); err != nil {
		return Coverage{}, fmt.Errorf("gdalbuildvrt: %w: %s", err, strings.TrimSpace(out))
	}
	if onProgress != nil {
		onProgress(len(tiles), total)
	}

	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		return Coverage{}, err
	}
	// ENVI output is the flat north-up little-endian float32 blob the geogrid reader expects. -r max keeps
	// the tallest canopy per output cell (obstruction is worst-case, not a mean).
	warpArgs := []string{
		"-overwrite", "-t_srs", "EPSG:4326",
		"-te", ftoa(b.MinLon), ftoa(b.MinLat), ftoa(b.MaxLon), ftoa(b.MaxLat),
		"-tr", ftoa(resDeg), ftoa(resDeg),
		"-r", "max", "-ot", "Float32", "-of", "ENVI",
		"--config", "GDAL_DISABLE_READDIR_ON_OPEN", "EMPTY_DIR",
		"--config", "GDAL_HTTP_MAX_RETRY", "3", "--config", "GDAL_HTTP_RETRY_DELAY", "2",
		vrt, binPath,
	}
	if out, err := runGDAL(ctx, "gdalwarp", warpArgs...); err != nil {
		return Coverage{}, fmt.Errorf("gdalwarp: %w: %s", err, strings.TrimSpace(out))
	}

	meta, err := metaFromGDAL(ctx, binPath)
	if err != nil {
		return Coverage{}, err
	}
	side := strings.TrimSuffix(binPath, filepath.Ext(binPath)) + ".json"
	mb, err := json.Marshal(meta)
	if err != nil {
		return Coverage{}, fmt.Errorf("marshal canopy sidecar: %w", err)
	}
	if err := os.WriteFile(side, mb, 0o644); err != nil {
		return Coverage{}, err
	}
	if onProgress != nil {
		onProgress(total, total)
	}
	return coverageFromMeta(meta, binPath), nil
}

// metaFromGDAL reads the written grid's dimensions + geo bounds via `gdalinfo -json` (no jq needed) and
// returns the geogrid.Meta sidecar (unit = meters).
func metaFromGDAL(ctx context.Context, binPath string) (geogrid.Meta, error) {
	out, err := runGDAL(ctx, "gdalinfo", "-json", binPath)
	if err != nil {
		return geogrid.Meta{}, fmt.Errorf("gdalinfo: %w: %s", err, strings.TrimSpace(out))
	}
	var gi struct {
		Size              [2]int `json:"size"`
		CornerCoordinates struct {
			UpperLeft  [2]float64 `json:"upperLeft"`
			LowerRight [2]float64 `json:"lowerRight"`
		} `json:"cornerCoordinates"`
		Bands []struct {
			NoDataValue *float64 `json:"noDataValue"`
		} `json:"bands"`
	}
	if err := json.Unmarshal([]byte(out), &gi); err != nil {
		return geogrid.Meta{}, fmt.Errorf("parse gdalinfo: %w", err)
	}
	if gi.Size[0] < 2 || gi.Size[1] < 2 {
		return geogrid.Meta{}, fmt.Errorf("canopy build produced an empty grid")
	}
	nodata := -1.0
	if len(gi.Bands) > 0 && gi.Bands[0].NoDataValue != nil {
		nodata = *gi.Bands[0].NoDataValue
	}
	return geogrid.Meta{
		Rows:   gi.Size[1],
		Cols:   gi.Size[0],
		LatMax: gi.CornerCoordinates.UpperLeft[1],
		LatMin: gi.CornerCoordinates.LowerRight[1],
		LonMin: gi.CornerCoordinates.UpperLeft[0],
		LonMax: gi.CornerCoordinates.LowerRight[0],
		Unit:   "meters",
		NoData: nodata,
	}, nil
}

func coverageFromMeta(m geogrid.Meta, binPath string) Coverage {
	cov := Coverage{
		Present: true,
		MinLat:  m.LatMin, MinLon: m.LonMin, MaxLat: m.LatMax, MaxLon: m.LonMax,
		Rows: m.Rows, Cols: m.Cols, Unit: m.Unit,
	}
	if fi, err := os.Stat(binPath); err == nil {
		cov.BuiltAtMs = fi.ModTime().UnixMilli()
	}
	return cov
}

func runGDAL(ctx context.Context, bin string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out), err
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
