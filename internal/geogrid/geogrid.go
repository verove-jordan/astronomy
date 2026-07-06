// Package geogrid reads an equirectangular (EPSG:4326) raster stored as a row-major block of
// little-endian float32 cells plus a JSON sidecar. It is the shared offline-atlas reader behind the
// light-pollution atlas and the tree-canopy atlas: row 0 is the northern edge (LatMax), the last row is
// LatMin; column 0 is LonMin, the last column is LonMax. Loading soft-fails to nil, so every caller can
// treat a missing or malformed atlas as simply "no data".
package geogrid

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Meta is the JSON sidecar describing the companion binary grid.
type Meta struct {
	Rows   int     `json:"rows"`
	Cols   int     `json:"cols"`
	LatMin float64 `json:"lat_min"`
	LatMax float64 `json:"lat_max"`
	LonMin float64 `json:"lon_min"`
	LonMax float64 `json:"lon_max"`
	Unit   string  `json:"unit"`   // domain-specific: "sqm" | "bortle" | "mcd" | "radiance" | "meters" | …
	NoData float64 `json:"nodata"` // sentinel cell value to ignore (e.g. -1)
}

// Grid is an offline raster, read on demand (ReadAt of the bilinear neighbours) so a multi-hundred-MB grid
// never has to be held in RAM. ram is an optional fully-loaded copy for hot per-pixel sampling (tile
// rendering): EnsureRAM populates it once when the grid fits under ramCellCap.
type Grid struct {
	f    *os.File
	Meta Meta
	ram  []float32 // nil until EnsureRAM loads it (only for grids below ramCellCap)
}

// ramCellCap bounds the fully-in-RAM copy to ~256 MB (a regional France/Europe atlas is small; only a
// whole-world atlas exceeds this and keeps using ReadAt).
const ramCellCap = 64 << 20

// Load opens the raster at binPath plus its `<name>.json` sidecar. It soft-fails to nil (no grid) when
// either file is missing or malformed — every caller then skips the offline step.
func Load(binPath string) *Grid {
	side := strings.TrimSuffix(binPath, filepath.Ext(binPath)) + ".json"
	sb, err := os.ReadFile(side)
	if err != nil {
		return nil
	}
	var meta Meta
	if err := json.Unmarshal(sb, &meta); err != nil {
		return nil
	}
	if meta.Rows < 2 || meta.Cols < 2 || meta.LatMax <= meta.LatMin || meta.LonMax <= meta.LonMin {
		return nil
	}
	f, err := os.Open(binPath)
	if err != nil {
		return nil
	}
	if fi, err := f.Stat(); err != nil || fi.Size() < int64(meta.Rows)*int64(meta.Cols)*4 {
		_ = f.Close()
		return nil
	}
	return &Grid{f: f, Meta: meta}
}

// EnsureRAM loads the entire grid into memory once (for fast per-pixel sampling) when it fits under
// ramCellCap. Returns whether an in-RAM copy is available.
func (g *Grid) EnsureRAM() bool {
	if g.ram != nil {
		return true
	}
	n := g.Meta.Rows * g.Meta.Cols
	if n <= 0 || n > ramCellCap {
		return false
	}
	buf := make([]byte, n*4)
	if _, err := g.f.ReadAt(buf, 0); err != nil {
		return false
	}
	grid := make([]float32, n)
	for i := range grid {
		grid[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	g.ram = grid
	return true
}

// SampleBilinear bilinearly samples the raw grid value at (lat, lon). It returns ok=false when the point
// is outside the covered region or all four neighbours are nodata.
func (g *Grid) SampleBilinear(lat, lon float64) (float64, bool) {
	r0, c0, r1, c1, dr, dc, ok := g.neighbours(lat, lon)
	if !ok {
		return 0, false
	}
	var sum, wsum float64
	for _, p := range [4]struct {
		r, c int
		w    float64
	}{
		{r0, c0, (1 - dr) * (1 - dc)},
		{r0, c1, (1 - dr) * dc},
		{r1, c0, dr * (1 - dc)},
		{r1, c1, dr * dc},
	} {
		v, ok := g.cell(p.r, p.c)
		if !ok || p.w == 0 {
			continue
		}
		sum += float64(v) * p.w
		wsum += p.w
	}
	if wsum == 0 {
		return 0, false
	}
	return sum / wsum, true
}

// SampleMax returns the largest valid value among the four cells surrounding (lat, lon) — the worst-case
// sample, used for obstruction data (canopy height) where averaging would hide the tallest blocker. It
// returns ok=false when the point is outside coverage or all four neighbours are nodata.
func (g *Grid) SampleMax(lat, lon float64) (float64, bool) {
	r0, c0, r1, c1, _, _, ok := g.neighbours(lat, lon)
	if !ok {
		return 0, false
	}
	best, found := 0.0, false
	for _, p := range [4][2]int{{r0, c0}, {r0, c1}, {r1, c0}, {r1, c1}} {
		v, ok := g.cell(p[0], p[1])
		if !ok {
			continue
		}
		if !found || float64(v) > best {
			best, found = float64(v), true
		}
	}
	return best, found
}

// neighbours locates the four bilinear-neighbour cells at (lat, lon) plus the fractional offsets (dr, dc)
// into that 2×2 block. ok=false when the point is outside the covered region.
func (g *Grid) neighbours(lat, lon float64) (r0, c0, r1, c1 int, dr, dc float64, ok bool) {
	m := g.Meta
	if lat < m.LatMin || lat > m.LatMax || lon < m.LonMin || lon > m.LonMax {
		return 0, 0, 0, 0, 0, 0, false
	}
	fr := (m.LatMax - lat) / (m.LatMax - m.LatMin) * float64(m.Rows-1)
	fc := (lon - m.LonMin) / (m.LonMax - m.LonMin) * float64(m.Cols-1)
	r0, c0 = int(math.Floor(fr)), int(math.Floor(fc))
	r1, c1 = min(r0+1, m.Rows-1), min(c0+1, m.Cols-1)
	return r0, c0, r1, c1, fr - float64(r0), fc - float64(c0), true
}

// cell reads one float32 grid cell, reporting ok=false on a read error or a nodata sentinel. It reads from
// the in-RAM copy when EnsureRAM has loaded it, else from disk via ReadAt.
func (g *Grid) cell(r, c int) (float32, bool) {
	var v float32
	if g.ram != nil {
		v = g.ram[r*g.Meta.Cols+c]
	} else {
		off := (int64(r)*int64(g.Meta.Cols) + int64(c)) * 4
		var buf [4]byte
		if _, err := g.f.ReadAt(buf[:], off); err != nil {
			return 0, false
		}
		v = math.Float32frombits(binary.LittleEndian.Uint32(buf[:]))
	}
	if math.IsNaN(float64(v)) || float64(v) == g.Meta.NoData {
		return 0, false
	}
	return v, true
}
