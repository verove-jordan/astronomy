package lightpollution

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// This file builds the OFFLINE atlas from David Lorenz's light-pollution model
// (https://djlorenz.github.io/astronomy/lp/). Unlike NASA Black Marble — an 8-bit night-lights
// visualization we can only map coarsely to SQM — the djlorenz grid is a propagation-modeled artificial/
// natural sky-brightness RATIO (the atmospheric skyglow is already integrated), so rural France correctly
// lands around Bortle 2-3. The decode below is a direct port of the reference `LpAtlasInBounds` decoder in
// .../lp/overlay/dark.html. Data is © David Lorenz, used with attribution for personal/non-commercial use.

const (
	djTileDeg     = 5.0              // each tile spans 5° × 5°
	djPxPerDeg    = 120.0            // 120 samples per degree (~30 arcsec)
	djTilePx      = 600              // samples per tile side (5° × 120)
	djLatOrigin   = 65.0             // tile row 1 starts at lat −65°
	djHalfCell    = 0.5 / djPxPerDeg // pixel centres sit half a cell inside the tile edge
	djMaxTileX    = 72               // 360° / 5°
	djMaxTileY    = 28               // (75 − −65)° / 5°
	djDefaultYear = 2024
)

// Bounds is a geographic bounding box in degrees (EPSG:4326).
type Bounds struct {
	MinLat, MinLon, MaxLat, MaxLon float64
}

// RegionBounds maps the user-facing region presets to bounding boxes. "france" is the default; "world" is
// the full djlorenz extent (lon capped just under +180 so the east edge lands in tile 72, not wrapping to
// tile 1). Callers may also pass an explicit Bounds instead of a preset.
var RegionBounds = map[string]Bounds{
	"france": {MinLat: 41.0, MinLon: -5.5, MaxLat: 51.5, MaxLon: 9.8},
	"europe": {MinLat: 35.0, MinLon: -11.0, MaxLat: 60.0, MaxLon: 20.0},
	"world":  {MinLat: -65.0, MinLon: -180.0, MaxLat: 74.999, MaxLon: 179.999},
}

// DjlorenzTileURL returns the download URL for one 5°×5° binary tile of the given year.
func DjlorenzTileURL(year, tilex, tiley int) string {
	return fmt.Sprintf("https://djlorenz.github.io/astronomy/binary_tiles/%d/binary_tile_%d_%d.dat.gz",
		year, tilex, tiley)
}

// djTileX/djTileY map a coordinate to its 1-based tile column/row (matching the reference JS: longitude is
// measured east from the date line, latitude north from −65°).
func djTileX(lon float64) int { return int(math.Floor(posMod(lon+180, 360)/djTileDeg)) + 1 }
func djTileY(lat float64) int { return int(math.Floor((lat+djLatOrigin)/djTileDeg)) + 1 }

// posMod is a non-negative modulo (Go's math.Mod keeps the sign of the dividend).
func posMod(n, m float64) float64 { return math.Mod(math.Mod(n, m)+m, m) }

// djTileRange returns the (clamped, 1-based) inclusive tile column/row span covering b. ok=false when the
// box selects no valid tile.
func djTileRange(b Bounds) (txMin, txMax, tyMin, tyMax int, ok bool) {
	txMin = clampInt(djTileX(b.MinLon), 1, djMaxTileX)
	txMax = clampInt(djTileX(b.MaxLon), 1, djMaxTileX)
	tyMin = clampInt(djTileY(b.MinLat), 1, djMaxTileY)
	tyMax = clampInt(djTileY(b.MaxLat), 1, djMaxTileY)
	return txMin, txMax, tyMin, tyMax, txMax >= txMin && tyMax >= tyMin
}

// compressedToSQM converts a reconstructed djlorenz "compressed" value to zenith sky brightness in
// mag/arcsec². It is the exact reference chain: ratio = (5/195)(e^{0.0195·x}−1); SQM = 22 − 2.5·log10(1+r).
// A pristine site (x=0) → 22.0; ratio 1 (artificial = natural) → 21.25. Negative ratios (rare rounding
// undershoot in dark cells) are floored at 0 so the sky never reads brighter-than-pristine.
func compressedToSQM(x int) float32 {
	ratio := (5.0 / 195.0) * (math.Exp(0.0195*float64(x)) - 1.0)
	if ratio < 0 {
		ratio = 0
	}
	return float32(pristineSQM - 2.5*math.Log10(1.0+ratio))
}

// decodeDjlorenzTile reconstructs one 600×600 tile of SQM values from its gzipped delta encoding. The
// returned slice is row-major with iy=south→north, ix=west→east (natural tile order), length 600·600.
//
// Encoding (per the reference decoder): after gunzip the bytes are read as signed int8. The SW corner is a
// 2-byte base (128·d[0]+d[1]); thereafter every sample is a 1-byte delta relative to its neighbour — up the
// first column by latitude, then along each row by longitude.
func decodeDjlorenzTile(gz []byte) ([]float32, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("gunzip djlorenz tile: %w", err)
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("read djlorenz tile: %w", err)
	}
	const n = djTilePx
	if len(raw) < n*n+1 {
		return nil, fmt.Errorf("djlorenz tile too short: %d bytes (want ≥ %d)", len(raw), n*n+1)
	}
	d := func(i int) int { return int(int8(raw[i])) } // signed int8, matching the JS Int8Array

	base := 128*d(0) + d(1)

	// First column (ix=1): cumulative latitude deltas from the SW corner northward.
	col1 := make([]int, n+1) // 1-based
	col1[1] = base
	for iy := 2; iy <= n; iy++ {
		col1[iy] = col1[iy-1] + d(n*(iy-1)+1)
	}

	grid := make([]float32, n*n)
	for iy := 1; iy <= n; iy++ {
		v := col1[iy]
		grid[(iy-1)*n] = compressedToSQM(v) // ix=1
		for ix := 2; ix <= n; ix++ {
			v += d(n*(iy-1) + ix)
			grid[(iy-1)*n+(ix-1)] = compressedToSQM(v)
		}
	}
	return grid, nil
}

// TileFetcher fetches the raw gzipped bytes of one tile; injected so buildAtlasGrid stays offline/testable.
// It should return an error only for a hard failure; a nil error with a decodable body is placed into the
// grid, and any error leaves that tile's cells as nodata (the reader then skips them).
type TileFetcher func(tilex, tiley int) ([]byte, error)

// buildAtlasGrid downloads and stitches the djlorenz tiles covering b into one north-up EPSG:4326 SQM grid
// plus its atlasMeta. The grid keeps the native 120 px/° resolution (no resampling); the covered area is
// rounded out to whole 5° tiles (a small margin around the requested box). Tiles that fail to fetch/decode
// are left as nodata. Returns an error only when the box selects no valid tiles.
func buildAtlasGrid(b Bounds, fetch TileFetcher) ([]float32, atlasMeta, error) {
	txMin, txMax, tyMin, tyMax, ok := djTileRange(b)
	if !ok {
		return nil, atlasMeta{}, fmt.Errorf("bounds select no djlorenz tiles: %+v", b)
	}

	nx := (txMax - txMin + 1) * djTilePx
	ny := (tyMax - tyMin + 1) * djTilePx
	cells := make([]float32, nx*ny)
	for i := range cells {
		cells[i] = -1 // nodata until a tile fills it
	}

	for ty := tyMin; ty <= tyMax; ty++ {
		for tx := txMin; tx <= txMax; tx++ {
			raw, err := fetch(tx, ty)
			if err != nil {
				continue // leave nodata; the reader falls back for these cells
			}
			grid, err := decodeDjlorenzTile(raw)
			if err != nil {
				continue
			}
			placeTile(cells, nx, grid, tx-txMin, tyMax-ty)
		}
	}

	meta := atlasMeta{
		Rows:   ny,
		Cols:   nx,
		LatMax: djTileDeg*float64(tyMax) - djLatOrigin - djHalfCell,
		LatMin: djTileDeg*float64(tyMin-1) - djLatOrigin + djHalfCell,
		LonMin: djTileDeg*float64(txMin-1) - 180 + djHalfCell,
		LonMax: djTileDeg*float64(txMax) - 180 - djHalfCell,
		Unit:   "sqm",
		NoData: -1,
	}
	return cells, meta, nil
}

// placeTile copies one decoded tile (south-up, west-east) into the big north-up grid. col/row are the
// tile's 0-based position within the mosaic (row 0 = northmost tile).
func placeTile(cells []float32, nx int, grid []float32, tileCol, tileRow int) {
	const n = djTilePx
	for iy := 1; iy <= n; iy++ {
		bigRow := tileRow*n + (n - iy) // flip south-up tile rows to north-up mosaic rows
		for ix := 1; ix <= n; ix++ {
			bigCol := tileCol*n + (ix - 1)
			cells[bigRow*nx+bigCol] = grid[(iy-1)*n+(ix-1)]
		}
	}
}

// writeAtlasFiles writes the grid + sidecar as atlas.bin / atlas.json in dir, atomically (temp+rename) so a
// concurrent reader never sees a half-written atlas. The binary is little-endian float32, row-major.
func writeAtlasFiles(dir string, meta atlasMeta, cells []float32) error {
	if len(cells) != meta.Rows*meta.Cols {
		return fmt.Errorf("atlas cell count %d != rows·cols %d", len(cells), meta.Rows*meta.Cols)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create atlas dir: %w", err)
	}
	buf := make([]byte, len(cells)*4)
	for i, v := range cells {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	if err := writeFileAtomic(filepath.Join(dir, "atlas.bin"), buf); err != nil {
		return err
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal atlas sidecar: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, "atlas.json"), mb)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
