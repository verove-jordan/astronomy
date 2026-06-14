// Package report renders an inventory (and, later, pipeline runs) as human-readable text,
// JSON, and markdown.
package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// InventoryJSON serializes an inventory as indented JSON.
func InventoryJSON(inv *inspect.Inventory) ([]byte, error) {
	return json.MarshalIndent(inv, "", "  ")
}

// InventoryText renders an inventory as an aligned, human-readable summary.
func InventoryText(inv *inspect.Inventory) string {
	var b strings.Builder
	counts := inv.CountsByType()

	fmt.Fprintf(&b, "AstroStack inventory: %s\n", inv.Root)
	fmt.Fprintf(&b, "Frames: %d  (light %d · dark %d · flat %d · dark-flat %d · bias %d)  Videos: %d\n\n",
		len(inv.Frames), counts[inspect.Light], counts[inspect.Dark], counts[inspect.Flat],
		counts[inspect.DarkFlat], counts[inspect.Bias], len(inv.Videos))

	writeLightSets(&b, inv)
	writeCalibSets(&b, inv)
	writeVideos(&b, inv)
	writeWarnings(&b, inv)
	return b.String()
}

func writeLightSets(b *strings.Builder, inv *inspect.Inventory) {
	lights := inv.SetsOfType(inspect.Light)
	if len(lights) == 0 {
		return
	}
	b.WriteString("Light sets:\n")
	tw := tabwriter.NewWriter(b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  OBJECT\tFILTER\tEXPOSURE\tCOUNT\tINTEGRATION\tGAIN\tOFFSET\tTEMP")
	for _, s := range lights {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\t%d\t%d\t%s\n",
			dash(s.Key.Object), dash(s.Key.Filter), humanizeMs(s.Key.ExposureMs), s.Count,
			humanizeMs(s.TotalIntegrationMs), s.Key.Gain, s.Key.Offset, temp(s.Key))
	}
	tw.Flush()
	b.WriteString("\n")
}

func writeCalibSets(b *strings.Builder, inv *inspect.Inventory) {
	var calib []inspect.Set
	for _, t := range []inspect.FrameType{inspect.Dark, inspect.Flat, inspect.DarkFlat, inspect.Bias} {
		calib = append(calib, inv.SetsOfType(t)...)
	}
	if len(calib) == 0 {
		return
	}
	b.WriteString("Calibration sets:\n")
	tw := tabwriter.NewWriter(b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  TYPE\tFILTER\tEXPOSURE\tCOUNT\tGAIN\tOFFSET\tTEMP")
	for _, s := range calib {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			s.Key.Type, dash(s.Key.Filter), exposure(s.Key), s.Count,
			s.Key.Gain, s.Key.Offset, temp(s.Key))
	}
	tw.Flush()
	b.WriteString("\n")
}

func writeVideos(b *strings.Builder, inv *inspect.Inventory) {
	if len(inv.Videos) == 0 {
		return
	}
	b.WriteString("Videos:\n")
	for _, v := range inv.Videos {
		fmt.Fprintf(b, "  %s\n", v.Path)
	}
	b.WriteString("\n")
}

func writeWarnings(b *strings.Builder, inv *inspect.Inventory) {
	if len(inv.Warnings) == 0 {
		return
	}
	b.WriteString("Warnings:\n")
	for _, w := range inv.Warnings {
		fmt.Fprintf(b, "  ⚠ %s\n", w)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func temp(k inspect.SetKey) string {
	if k.Type == inspect.Bias {
		return "-"
	}
	return fmt.Sprintf("%d°C", k.TempBucket)
}

func exposure(k inspect.SetKey) string {
	if k.Type == inspect.Bias {
		return "-"
	}
	return humanizeMs(k.ExposureMs)
}

func humanizeMs(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.3gs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
