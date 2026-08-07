package solar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// triage_live_test.go runs the real triage over a real capture folder. It needs host tools (`sips`
// or dcraw for HEIC/DNG, ffmpeg for clips) and several GB of frames, so it is opt-in:
//
//	ASTRO_SOLAR_LIVE=input/2026_07_30_SUN go test ./internal/solar -run Live -v
//
// It asserts the properties that must hold for any solar folder rather than pinning one session's
// numbers, and prints the full grouping so a human can sanity-check it against what they shot.
func TestTriage_Live(t *testing.T) {
	root := os.Getenv("ASTRO_SOLAR_LIVE")
	if root == "" {
		t.Skip("set ASTRO_SOLAR_LIVE=<capture dir> to run the live triage")
	}
	if !filepath.IsAbs(root) {
		wd, err := os.Getwd()
		require.NoError(t, err)
		root = filepath.Join(wd, "..", "..", root)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()
	rep, err := Triage(ctx, root, Options{Workers: 6})
	require.NoError(t, err)
	t.Logf("triaged %d files in %s", rep.Files, time.Since(start).Round(time.Second))

	printReport(t, rep)

	require.NotEmpty(t, rep.Groups, "a solar folder must yield at least one group")
	for _, g := range rep.Groups {
		require.NotEmpty(t, g.ID, "every group needs an exclusion token")
		require.Greater(t, g.DiscRadius, 0.0, "group %s has no measured disc", g.ID)
		// The grouping invariant: every member of a group agrees on scale to within the tolerance.
		for _, m := range g.Members {
			ratio := m.Disc.R / g.DiscRadius
			require.InDelta(t, 1.0, ratio, maxGroupSpread,
				"group %s member %s is %.1f%% off the group radius", g.ID, filepath.Base(m.Path), 100*(ratio-1))
		}
	}
	// Group IDs must be unique — they are exclusion tokens.
	seen := map[string]bool{}
	for _, g := range rep.Groups {
		require.False(t, seen[g.ID], "duplicate group id %s", g.ID)
		seen[g.ID] = true
	}
}

// printReport renders the triage result as the operator-facing table it will become in the UI.
func printReport(t *testing.T, rep *Report) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "\n%d file(s) under %s\n", rep.Files, rep.Root)
	for _, w := range rep.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", w)
	}
	for _, g := range rep.Groups {
		mark := " "
		if g.Stackable {
			mark = "*"
		}
		fmt.Fprintf(&b, "\n%s %s\n    %s\n", mark, g.ID, g.Label)
		fmt.Fprintf(&b, "    files=%d frames=%d radius=%.1fpx scale=%.2f\"/px detail=%.4g span=%s nearest=%.1f%%\n",
			g.Files, g.Frames, g.DiscRadius, g.ArcsecPerPx, g.Detail,
			(time.Duration(g.SpanMs) * time.Millisecond).Round(time.Second), g.NearestPct)
		for _, n := range g.Notes {
			fmt.Fprintf(&b, "    note: %s\n", n)
		}
		for _, m := range g.Members {
			status := "keep"
			if m.Rejected {
				status = "DROP"
			}
			fmt.Fprintf(&b, "      %s %-16s r=%7.1f iso=%-5d exp=%-6dms fl=%-4d clip=%.3f med=%.4g det=%.4g",
				status, filepath.Base(m.Path), m.Disc.R, m.ISO, m.ExposureMs, m.FocalLength35mm,
				m.ClippedFrac, m.OnDiscMedian, m.Detail)
			if m.Frames > 1 {
				fmt.Fprintf(&b, " frames=%d", m.Frames)
			}
			fmt.Fprintln(&b)
			for _, r := range m.Reasons {
				fmt.Fprintf(&b, "             ↳ %s: %s\n", r.Code, r.Text)
			}
		}
	}
	if len(rep.Unusable) > 0 {
		fmt.Fprintf(&b, "\n  unusable (%d)\n", len(rep.Unusable))
		for _, m := range rep.Unusable {
			why := "no limb"
			if len(m.Reasons) > 0 {
				why = m.Reasons[0].Code + ": " + m.Reasons[0].Text
			}
			fmt.Fprintf(&b, "      %-16s %s\n", filepath.Base(m.Path), why)
		}
	}
	t.Log(b.String())

	if dump := os.Getenv("ASTRO_SOLAR_DUMP"); dump != "" {
		blob, err := json.MarshalIndent(rep, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dump, blob, 0o644))
		t.Logf("report written to %s", dump)
	}
}
