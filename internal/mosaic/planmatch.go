package mosaic

// Plan-tile matching and panel ordering/labeling: detected panels take their plan tile's Folder
// label when one sits within range, so re-runs and partial captures keep stable panel names.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// matchFracFOV is the plan-tile match radius as a fraction of the panel field of view.
const matchFracFOV = 0.5

// matchPlan pairs detected panels with plan tiles by angular distance (< matchFracFOV×fovDeg),
// greedily by closeness so labels stay stable run over run. Matched panels take their tile's
// Folder label; leftover tiles and unmatched panels are warned about.
func matchPlan(panels []Panel, plan *Plan, fovDeg float64) []string {
	if plan == nil || len(plan.Tiles) == 0 {
		return nil
	}
	if fovDeg <= 0 {
		return []string{"cannot match panels to the mosaic plan without a field-of-view estimate"}
	}
	type cand struct {
		p, t int
		sep  float64
	}
	var cands []cand
	for pi := range panels {
		if !panels[pi].HasCenter {
			continue
		}
		for ti := range plan.Tiles {
			sep := astro.AngularSeparation(panels[pi].RA, panels[pi].Dec, plan.Tiles[ti].RA, plan.Tiles[ti].Dec)
			if sep < matchFracFOV*fovDeg {
				cands = append(cands, cand{pi, ti, sep})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sep < cands[j].sep })
	panelDone := make([]bool, len(panels))
	tileDone := make([]bool, len(plan.Tiles))
	for _, c := range cands {
		if panelDone[c.p] || tileDone[c.t] {
			continue
		}
		panelDone[c.p], tileDone[c.t] = true, true
		panels[c.p].PlanTile = &plan.Tiles[c.t]
		panels[c.p].Label = plan.Tiles[c.t].Folder
	}
	return planMatchWarnings(plan, tileDone, len(panels)-countTrue(panelDone))
}

// planMatchWarnings names the plan tiles no panel claimed and counts the panels no tile claimed.
func planMatchWarnings(plan *Plan, tileDone []bool, extraPanels int) []string {
	var warns []string
	var leftover []string
	for ti, done := range tileDone {
		if !done {
			leftover = append(leftover, plan.Tiles[ti].Folder)
		}
	}
	if len(leftover) > 0 {
		warns = append(warns, fmt.Sprintf("plan tile(s) %s have no matching captured panel", strings.Join(leftover, ", ")))
	}
	if extraPanels > 0 {
		warns = append(warns, fmt.Sprintf("%d detected panel(s) match no plan tile", extraPanels))
	}
	return warns
}

// orderAndLabel sorts panels — plan order first for matched panels, discovery order for the rest —
// then labels the unmatched ones p01-style by final position, skipping labels a plan tile took.
func orderAndLabel(panels []Panel) {
	sort.SliceStable(panels, func(i, j int) bool {
		ti, tj := panels[i].PlanTile, panels[j].PlanTile
		if (ti != nil) != (tj != nil) {
			return ti != nil
		}
		if ti != nil && tj != nil {
			return ti.Order < tj.Order
		}
		return false // stable: keep discovery order among unmatched panels
	})
	used := make(map[string]bool, len(panels))
	for i := range panels {
		used[panels[i].Label] = true
	}
	next := 0
	for i := range panels {
		if panels[i].Label != "" {
			continue
		}
		for {
			next++
			if label := fmt.Sprintf("p%02d", next); !used[label] {
				panels[i].Label = label
				break
			}
		}
	}
}

func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}
