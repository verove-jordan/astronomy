package lightpollution

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// atlasMeta is the JSON sidecar describing the companion binary grid. The grid is a row-major block of
// little-endian float32 cells in an equirectangular (EPSG:4326) layout: row 0 is the northern edge
// (LatMax), the last row is LatMin; column 0 is LonMin, the last column is LonMax.
type atlasMeta struct {
	Rows   int     `json:"rows"`
	Cols   int     `json:"cols"`
	LatMin float64 `json:"lat_min"`
	LatMax float64 `json:"lat_max"`
	LonMin float64 `json:"lon_min"`
	LonMax float64 `json:"lon_max"`
	Unit   string  `json:"unit"`   // "sqm" | "bortle" | "mcd" | "radiance"
	NoData float64 `json:"nodata"` // sentinel cell value to ignore (e.g. -1)
}

// atlas is the offline light-pollution raster, read on demand (ReadAt of the four bilinear neighbours)
// so a multi-hundred-MB grid never has to be held in RAM. ram is an optional fully-loaded copy: tile
// rendering samples ~65k pixels per tile, so ReadAt-per-pixel would be far too many syscalls; ensureRAM
// loads the whole grid once for a regional atlas (small), and cell() then reads from it.
type atlas struct {
	f    *os.File
	meta atlasMeta
	ram  []float32 // nil until ensureRAM loads it (only for grids below ramCellCap)
}

// ramCellCap bounds the fully-in-RAM copy to ~256 MB (a regional France/Europe atlas is ≤ ~12M cells;
// only a whole-world atlas exceeds this and keeps using ReadAt).
const ramCellCap = 64 << 20

// ensureRAM loads the entire grid into memory once (for fast per-pixel tile rendering) when it fits under
// ramCellCap. Returns whether an in-RAM copy is available.
func (a *atlas) ensureRAM() bool {
	if a.ram != nil {
		return true
	}
	n := a.meta.Rows * a.meta.Cols
	if n <= 0 || n > ramCellCap {
		return false
	}
	buf := make([]byte, n*4)
	if _, err := a.f.ReadAt(buf, 0); err != nil {
		return false
	}
	grid := make([]float32, n)
	for i := range grid {
		grid[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	a.ram = grid
	return true
}

// loadAtlas opens the atlas at binPath plus its `<name>.json` sidecar. It soft-fails to nil (no atlas)
// when either file is missing or malformed — the provider then skips the offline step.
func loadAtlas(binPath string) *atlas {
	side := strings.TrimSuffix(binPath, filepath.Ext(binPath)) + ".json"
	sb, err := os.ReadFile(side)
	if err != nil {
		return nil
	}
	var meta atlasMeta
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
	return &atlas{f: f, meta: meta}
}

// sampleSQM bilinearly samples the grid at (lat, lon) and converts to SQM. It returns ok=false when the
// point is outside the covered region or all four neighbours are nodata.
func (a *atlas) sampleSQM(lat, lon float64) (float64, bool) {
	m := a.meta
	if lat < m.LatMin || lat > m.LatMax || lon < m.LonMin || lon > m.LonMax {
		return 0, false
	}
	fr := (m.LatMax - lat) / (m.LatMax - m.LatMin) * float64(m.Rows-1)
	fc := (lon - m.LonMin) / (m.LonMax - m.LonMin) * float64(m.Cols-1)
	r0, c0 := int(math.Floor(fr)), int(math.Floor(fc))
	r1, c1 := min(r0+1, m.Rows-1), min(c0+1, m.Cols-1)
	dr, dc := fr-float64(r0), fc-float64(c0)

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
		v, ok := a.cell(p.r, p.c)
		if !ok || p.w == 0 {
			continue
		}
		sum += float64(v) * p.w
		wsum += p.w
	}
	if wsum == 0 {
		return 0, false
	}
	return valueToSQM(sum/wsum, m.Unit), true
}

// cell reads one float32 grid cell, reporting ok=false on a read error or a nodata sentinel. It reads from
// the in-RAM copy when ensureRAM has loaded it, else from disk via ReadAt.
func (a *atlas) cell(r, c int) (float32, bool) {
	var v float32
	if a.ram != nil {
		v = a.ram[r*a.meta.Cols+c]
	} else {
		off := (int64(r)*int64(a.meta.Cols) + int64(c)) * 4
		var buf [4]byte
		if _, err := a.f.ReadAt(buf[:], off); err != nil {
			return 0, false
		}
		v = math.Float32frombits(binary.LittleEndian.Uint32(buf[:]))
	}
	if math.IsNaN(float64(v)) || float64(v) == a.meta.NoData {
		return 0, false
	}
	return v, true
}
