package gimp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReduceStarsScript(t *testing.T) {
	res := &Result{Tif: "/o/final_reduced.tif", Png: "/o/final_reduced.png"}
	s := reduceStarsScript("/o/final.tif", "/o/final_starless.tif", 0.5, res)

	// Starless is the base; original (with stars) is layered on top at the reduce opacity.
	assert.Contains(t, s, `(gimp-file-load RUN-NONINTERACTIVE "/o/final_starless.tif"`)
	assert.Contains(t, s, `(gimp-file-load-layer RUN-NONINTERACTIVE image "/o/final.tif"`)
	assert.Contains(t, s, "(gimp-layer-set-opacity stars 50)")
	assert.Contains(t, s, `"/o/final_reduced.tif"`)
	assert.Contains(t, s, `"/o/final_reduced.png"`)
}

func TestReduceStarsScript_OpacityClamped(t *testing.T) {
	res := &Result{Tif: "/o/r.tif", Png: "/o/r.png"}
	assert.Contains(t, reduceStarsScript("/o/o.tif", "/o/s.tif", 1.8, res), "(gimp-layer-set-opacity stars 100)")
	assert.Contains(t, reduceStarsScript("/o/o.tif", "/o/s.tif", -0.5, res), "(gimp-layer-set-opacity stars 0)")
}
