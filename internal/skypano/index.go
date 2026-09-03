package skypano

import "math"

// pixelIndex is a uniform grid over the detections, so the coarse search can ask "is there a
// detection near this pixel" without walking thousands of them for each of thousands of grid points.
type pixelIndex struct {
	cell     float64
	minX     float64
	minY     float64
	cols     int
	rows     int
	buckets  [][]Detection
	haveData bool
}

func newPixelIndex(det []Detection, cell float64) *pixelIndex {
	if len(det) == 0 || cell <= 0 {
		return &pixelIndex{}
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, d := range det {
		minX, minY = math.Min(minX, d.X), math.Min(minY, d.Y)
		maxX, maxY = math.Max(maxX, d.X), math.Max(maxY, d.Y)
	}
	cols := int((maxX-minX)/cell) + 1
	rows := int((maxY-minY)/cell) + 1
	ix := &pixelIndex{cell: cell, minX: minX, minY: minY, cols: cols, rows: rows,
		buckets: make([][]Detection, cols*rows), haveData: true}
	for _, d := range det {
		c, r := ix.cellOf(d.X, d.Y)
		if c >= 0 {
			ix.buckets[r*cols+c] = append(ix.buckets[r*cols+c], d)
		}
	}
	return ix
}

func (ix *pixelIndex) cellOf(x, y float64) (col, row int) {
	if !ix.haveData {
		return -1, -1
	}
	c := int((x - ix.minX) / ix.cell)
	r := int((y - ix.minY) / ix.cell)
	if c < 0 || r < 0 || c >= ix.cols || r >= ix.rows {
		return -1, -1
	}
	return c, r
}

// near reports whether any detection lies within radius of (x,y).
func (ix *pixelIndex) near(x, y, radius float64) bool {
	if !ix.haveData {
		return false
	}
	r2 := radius * radius
	c0 := int((x - ix.minX - radius) / ix.cell)
	c1 := int((x - ix.minX + radius) / ix.cell)
	r0 := int((y - ix.minY - radius) / ix.cell)
	r1 := int((y - ix.minY + radius) / ix.cell)
	for r := max(r0, 0); r <= min(r1, ix.rows-1); r++ {
		for c := max(c0, 0); c <= min(c1, ix.cols-1); c++ {
			for _, d := range ix.buckets[r*ix.cols+c] {
				if (d.X-x)*(d.X-x)+(d.Y-y)*(d.Y-y) <= r2 {
					return true
				}
			}
		}
	}
	return false
}

// countMatches scores a camera by how many catalogue stars land near a detection.
func countMatches(c Camera, cat [][3]float64, idx *pixelIndex, radius float64) int {
	n := 0
	for _, v := range cat {
		x, y, ok := c.Project(v)
		if ok && idx.near(x, y, radius) {
			n++
		}
	}
	return n
}

// solveN solves the leading n-by-n system by Gaussian elimination with partial pivoting.
func solveN(a [maxFitParams][maxFitParams]float64, b [maxFitParams]float64, n int) ([maxFitParams]float64, bool) {
	var m [maxFitParams][maxFitParams + 1]float64
	for i := 0; i < n; i++ {
		copy(m[i][:n], a[i][:n])
		m[i][maxFitParams] = b[i]
	}
	for col := 0; col < n; col++ {
		p := col
		for r := col + 1; r < n; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[p][col]) {
				p = r
			}
		}
		if math.Abs(m[p][col]) < 1e-14 {
			return [maxFitParams]float64{}, false
		}
		m[col], m[p] = m[p], m[col]
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for k := col; k <= maxFitParams; k++ {
				m[r][k] -= f * m[col][k]
			}
		}
	}
	var out [maxFitParams]float64
	for i := 0; i < n; i++ {
		out[i] = m[i][maxFitParams] / m[i][i]
	}
	return out, true
}
