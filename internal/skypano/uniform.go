package skypano

// uniform.go equalises star density before quads are built.
//
// A quad is four stars found among each other's nearest neighbours, so a match needs the SAME four
// stars to be selected on both sides — which needs the two star lists to agree about which stars are
// there, locally. Taking "the N brightest overall" does not achieve that across this kind of field.
// A Milky Way panel varies in star density by about a factor of ten between the band and the
// corners, so an overall cut fills the band and starves the edges, and it does so differently on
// each side: the catalogue thins by magnitude, the image by a flux measurement that saturation and
// nebulosity have both disturbed. The neighbourhoods then disagree and no quad ever corresponds.
//
// Taking the brightest few per grid cell instead gives both sides the same, even density. This is
// what astrometry.net does when it builds its indexes, and for the same reason.

// selectUniform returns the indices of the brightest perCell points in each cell of a
// cellsX-by-cellsY grid spanning w-by-h. pts must be ordered brightest first.
func selectUniform(pts []Point, w, h float64, cellsX, cellsY, perCell int) []int {
	if len(pts) == 0 || cellsX < 1 || cellsY < 1 || perCell < 1 || w <= 0 || h <= 0 {
		return nil
	}
	count := make([]int, cellsX*cellsY)
	out := make([]int, 0, cellsX*cellsY*perCell)
	for i, p := range pts {
		cx := int(p.X / w * float64(cellsX))
		cy := int(p.Y / h * float64(cellsY))
		if cx < 0 || cy < 0 || cx >= cellsX || cy >= cellsY {
			continue
		}
		c := cy*cellsX + cx
		if count[c] >= perCell {
			continue
		}
		count[c]++
		out = append(out, i)
	}
	return out
}

// subset gathers the listed points, keeping the mapping back to the original indices.
func subset(pts []Point, idx []int) []Point {
	out := make([]Point, len(idx))
	for i, j := range idx {
		out[i] = pts[j]
	}
	return out
}
