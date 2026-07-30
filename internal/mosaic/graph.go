package mosaic

// Overlap-graph topology: anchor election, connected components, and the sky-leveling fallback
// for panels the graph cannot reach.

import (
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// skySampleCap bounds the per-panel pixel subsample behind skyOf.
const skySampleCap = 200_000

// anchorPanel picks the reference panel: the one with the most measured pairs (tie: lowest index).
func anchorPanel(n int, pairs []PairFit) int {
	counts := make([]int, n)
	for _, pr := range pairs {
		counts[pr.A]++
		counts[pr.B]++
	}
	best := 0
	for i := 1; i < n; i++ {
		if counts[i] > counts[best] {
			best = i
		}
	}
	return best
}

// components labels the connected components of the panel overlap graph via union-find.
func components(n int, pairs []PairFit) []int {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(a int) int {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	for _, pr := range pairs {
		ra, rb := find(pr.A), find(pr.B)
		if ra != rb {
			parent[rb] = ra
		}
	}
	comp := make([]int, n)
	for i := range comp {
		comp[i] = find(i)
	}
	return comp
}

// anchorComponentPairs keeps only the pairs inside the anchor's connected component. The least
// squares MUST be restricted to it: a disconnected component contributes an anchor-free Laplacian
// block, which is singular (its solution is defined only up to a constant) and would poison the
// whole solve. Island panels keep pinned neutral corrections until levelIslands replaces them.
func anchorComponentPairs(n, anchor int, pairs []PairFit) []PairFit {
	comp := components(n, pairs)
	kept := make([]PairFit, 0, len(pairs))
	for _, pr := range pairs {
		if comp[pr.A] == comp[anchor] {
			kept = append(kept, pr)
		}
	}
	return kept
}

// levelIslands handles panels disconnected from the anchor's overlap component: the graph solve
// cannot reach them, so each keeps gain 1 and gets the offset that levels its median sky onto the
// anchor component's corrected median sky. A warning names the island panels.
func levelIslands(panels []PanelImage, sol *PhotomSolution) {
	comp := components(len(panels), sol.Pairs)
	anchorComp := comp[sol.Anchor]
	islands := false
	for i := range panels {
		if comp[i] != anchorComp {
			islands = true
			break
		}
	}
	if !islands {
		return
	}
	var anchorSkies []float64
	for i := range panels {
		if comp[i] == anchorComp {
			anchorSkies = append(anchorSkies, sol.Gain[i]*skyOf(panels[i].Image)+sol.Offset[i])
		}
	}
	ref := medianF64(anchorSkies)
	var names []string
	for i := range panels {
		if comp[i] == anchorComp {
			continue
		}
		sol.Gain[i] = 1
		sol.Offset[i] = ref - skyOf(panels[i].Image)
		names = append(names, panels[i].Label)
	}
	sol.Warnings = append(sol.Warnings, fmt.Sprintf(
		"panel(s) %s share no usable overlap with the anchor — sky-leveled only (gain 1)", strings.Join(names, ", ")))
}

// skyOf estimates a panel's sky pedestal: the median of its positive pixels (subsampled).
func skyOf(im *fits.Image) float64 {
	sample := imgops.Subsample(im.Pix[0], skySampleCap)
	pos := make([]float32, 0, len(sample))
	for _, v := range sample {
		if v > 0 {
			pos = append(pos, v)
		}
	}
	if len(pos) == 0 {
		return 0
	}
	return imgops.Percentile(pos, 50)
}
