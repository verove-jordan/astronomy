package imgops

// BinaryDilation grows the true region by `iterations` steps of 4-connectivity, matching
// scipy.ndimage.binary_dilation with its default cross structuring element. The input is not
// modified.
func BinaryDilation(mask []bool, w, h, iterations int) []bool {
	cur := make([]bool, len(mask))
	copy(cur, mask)
	for it := 0; it < iterations; it++ {
		next := make([]bool, len(cur))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				if cur[i] {
					next[i] = true
					continue
				}
				if x > 0 && cur[i-1] || x < w-1 && cur[i+1] ||
					y > 0 && cur[i-w] || y < h-1 && cur[i+w] {
					next[i] = true
				}
			}
		}
		cur = next
	}
	return cur
}

// Label assigns a connected-component id (1..n) to each true pixel with 4-connectivity (0 for
// background), like scipy.ndimage.label. It returns the label grid and the component count.
func Label(mask []bool, w, h int) (labels []int, n int) {
	labels = make([]int, len(mask))
	parent := []int{0} // union-find; index 0 is background sentinel
	find := func(a int) int {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	// First pass: provisional labels + equivalences (check left and up neighbours).
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if !mask[i] {
				continue
			}
			left, up := 0, 0
			if x > 0 {
				left = labels[i-1]
			}
			if y > 0 {
				up = labels[i-w]
			}
			switch {
			case left == 0 && up == 0:
				id := len(parent)
				parent = append(parent, id)
				labels[i] = id
			case left != 0 && up != 0:
				labels[i] = left
				union(left, up)
			case left != 0:
				labels[i] = left
			default:
				labels[i] = up
			}
		}
	}
	// Second pass: flatten to root and renumber roots to 1..n.
	remap := make(map[int]int)
	for i, l := range labels {
		if l == 0 {
			continue
		}
		root := find(l)
		id, ok := remap[root]
		if !ok {
			n++
			id = n
			remap[root] = id
		}
		labels[i] = id
	}
	return labels, n
}
