package transient

import (
	"os"
	"strconv"
)

// Memory budgeting for the cross-frame mask. The validated pass holds every registered frame PLUS
// a residual plane per frame resident (~2·n·plane bytes): a 129-sub 16 MP anchored-canvas merge
// needs ~17 GiB structural — fine on a big host, fatal inside the containerized stack's VM (the
// engine was OOM-killed at ~30 GiB in a 35 GiB VM shared with other services). Above the budget
// the pipeline switches to the streamed variant instead of dying.

const defaultBudgetBytes = 8 << 30 // conservative fallback when the platform probe fails

// MemBudget returns the byte budget for the cross-frame mask: ASTRO_TRAIL_MASK_MEM_GB when set,
// else a platform-derived share of the machine's memory (60% of Linux MemAvailable — the
// containerized engine shares its VM with other services; half the physical memory on macOS).
func MemBudget() int64 {
	if gb, err := strconv.ParseFloat(os.Getenv("ASTRO_TRAIL_MASK_MEM_GB"), 64); err == nil && gb > 0 {
		return int64(gb * float64(1<<30))
	}
	if b := platformBudget(); b > 0 {
		return b
	}
	return defaultBudgetBytes
}

// evenIndices returns k evenly-spaced indices over [0, n) (all of them when k >= n) — the
// median/validation basis of the streamed mask.
func evenIndices(n, k int) []int {
	if k >= n {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, k)
	for i := 0; i < k; i++ {
		out[i] = i * n / k
	}
	return out
}
