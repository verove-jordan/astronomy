package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
)

func TestStepper(t *testing.T) {
	var events []Progress
	s := newStepper(func(p Progress) { events = append(events, p) }, 3)

	fwd := s.begin("stacking L")
	fwd(siril.Progress{Line: "10%"})
	s.begin("aligning channels")
	s.begin("composite (GIMP)")
	s.begin("export") // 4th begin: index clamps at total 3
	s.finish()

	var lines []string
	for _, e := range events {
		if e.Line != "" {
			lines = append(lines, e.Line)
		}
		assert.LessOrEqual(t, e.Index, 3, "index must clamp at the planned total")
		assert.Equal(t, 3, e.Total)
	}
	assert.Equal(t, []string{
		"▶ stacking L",
		"10%",
		"✓ stacking L done in 0s",
		"▶ aligning channels",
		"✓ aligning channels done in 0s",
		"▶ composite (GIMP)",
		"✓ composite (GIMP) done in 0s",
		"▶ export",
		"✓ export done in 0s",
	}, lines)

	// The step forwarder pins its own index even after later steps began.
	first := events[0]
	assert.Equal(t, 1, first.Index)
}

func TestBeginStep_NilStepperKeepsIndexlessLines(t *testing.T) {
	var events []Progress
	o := Options{OnProgress: func(p Progress) { events = append(events, p) }}
	fwd := o.beginStep("refine finish")
	fwd(siril.Progress{Line: "log line"})
	if assert.Len(t, events, 1) {
		assert.Equal(t, "refine finish", events[0].Step)
		assert.Zero(t, events[0].Index)
		assert.Zero(t, events[0].Total)
	}
}

func TestFinishStepPlan(t *testing.T) {
	gimpClient := &gimp.Client{}
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{
			"gimp path with denoise, starnet and star fix",
			Options{
				Gimp: gimpClient, Graxpert: &graxpert.Runner{}, Starnet: &starnet.Runner{},
				Preset: &mode.Preset{ColorDenoiseAI: true, StarReduce: 0.5, AutoFixStars: true},
			},
			[]string{"aligning channels", "combining channels + background", "AI colour denoise (GraXpert)",
				"colour calibration + stretch", "composite (GIMP)", "star reduction (StarNet++)",
				"star quality check", "export"},
		},
		{
			"plain gimp path",
			Options{Gimp: gimpClient, Preset: &mode.Preset{}},
			[]string{"aligning channels", "combining channels + background",
				"colour calibration + stretch", "composite (GIMP)", "export"},
		},
		{
			"no gimp falls back to the siril combine",
			Options{Preset: &mode.Preset{}},
			[]string{"aligning channels", "combining channels (Siril)", "export"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, finishStepPlan(tt.opts))
		})
	}
}
