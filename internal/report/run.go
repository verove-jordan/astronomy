package report

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/verove-jordan/astronomy/internal/pipeline"
)

// RunJSON serializes a pipeline result as indented JSON.
func RunJSON(r *pipeline.Result) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// RunText renders a pipeline result as an aligned, human-readable summary.
func RunText(r *pipeline.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AstroStack run\nInput : %s\nOutput: %s\n\n", r.InputDir, r.OutputDir)

	fmt.Fprintf(&b, "Masters built: %d\n", len(r.Masters))
	if len(r.Masters) > 0 {
		tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  TYPE\tFILTER\tEXPOSURE\tFRAMES\tFILE")
		for _, m := range r.Masters {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\n",
				m.Type, dash(m.Filter), humanizeMs(m.ExposureMs), m.FrameCount, filepath.Base(m.Path))
		}
		tw.Flush()
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "Channels: %d\n", len(r.Channels))
	tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  FILTER\tEXPOSURE\tIN\tSTACKED\tDARK\tFLAT\tBIAS\tOUTPUT")
	for _, c := range r.Channels {
		out := dash(filepath.Base(c.OutputPath))
		if c.Err != "" {
			out = "FAILED"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
			dash(c.Filter), humanizeMs(c.ExposureMs), c.InputFrames, c.StackedFrames,
			yesno(c.Selection.Dark != nil), yesno(c.Selection.Flat != nil),
			yesno(c.Selection.Bias != nil), out)
	}
	tw.Flush()

	for _, c := range r.Channels {
		for _, n := range c.Selection.Notes {
			fmt.Fprintf(&b, "  · %s: %s\n", dash(c.Filter), n)
		}
	}
	b.WriteString("\n")

	writeRejected(&b, r)

	if len(r.Warnings) > 0 {
		b.WriteString("Warnings:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "  ⚠ %s\n", w)
		}
	}
	return b.String()
}

func writeRejected(b *strings.Builder, r *pipeline.Result) {
	var rejected int
	for _, c := range r.Channels {
		for _, m := range c.Metrics {
			if m.Rejected {
				rejected++
			}
		}
	}
	if rejected == 0 {
		return
	}
	fmt.Fprintf(b, "Rejected sub-frames: %d\n", rejected)
	for _, c := range r.Channels {
		for _, m := range c.Metrics {
			if m.Rejected {
				fmt.Fprintf(b, "  ✗ [%s] %s — %s\n", dash(c.Filter), filepath.Base(m.Path), m.RejectReason)
			}
		}
	}
	b.WriteString("\n")
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
