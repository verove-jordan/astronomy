package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// sunseqrebuild.go builds a sequence again from the frames a finished run already extracted.
//
// Extracting them is the expensive half by a wide margin — decoding 60 000 frames of 4K ProRes took
// sixteen hours on the 12 Aug 2026 session, where everything downstream is minutes. So changing
// which phases the sheet shows, or how each panel is built, must not mean going back to the video.
// The run leaves its frames in <work>/sun_<runID>/ and its triage report in the run directory; that
// is enough to reconstruct exactly the frame list the run had, because the clock of a video frame is
// its container's creation time plus its own index over the frame rate — the same arithmetic ingest
// used, not a guess.

// scratchSuffix is the extension ingest writes its materialised frames with. The names themselves are
// <clip base>_<EXT>_<index>.fits.
const scratchSuffix = ".fits"

// rebuildSequence re-plans and re-renders a run's sheet from its own scratch.
func rebuildSequence(ctx context.Context, opts Options, runDir string, p solar.Preset, object string,
	say func(string)) ([]string, []string) {

	scratch := filepath.Join(opts.WorkDir, "sun_"+filepath.Base(runDir))
	report, err := readTriageReport(runDir)
	if err != nil {
		return nil, []string{"sun: phase sequence: rebuild: " + err.Error()}
	}
	group := solar.MergeGroups(stackableGroups(report))
	site, ok := sequenceSite(group, p)
	if !ok {
		return nil, []string{"sun: phase sequence: rebuild: the run's clips carry no location tag and no site_lat/site_lon was given"}
	}
	frames, warn := scratchFrames(scratch, group)
	if len(frames) == 0 {
		return nil, append(warn, fmt.Sprintf(
			"sun: phase sequence: rebuild: no extracted frame left in %s — the run's scratch is gone, so the video has to be read again",
			scratch))
	}
	say(fmt.Sprintf("rebuilding from %d frames already extracted to %s", len(frames), filepath.Base(scratch)))

	if again, reWarn := reextractCutClips(ctx, opts, group, p, scratch, frames, say); len(again) > 0 {
		frames = again
		warn = append(warn, reWarn...)
	} else {
		warn = append(warn, reWarn...)
	}

	outs, cWarn := composeSequence(ctx, frames, site, p, runDir, object, hydrateWindow(p), say)
	return outs, append(warn, cWarn...)
}

// readTriageReport reads back the triage a run persisted, which carries every clip's path, clock,
// frame rate and location tag.
func readTriageReport(runDir string) (*solar.Report, error) {
	b, err := os.ReadFile(filepath.Join(runDir, "triage.json"))
	if err != nil {
		return nil, err
	}
	var rep solar.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, err
	}
	if len(rep.Groups) == 0 {
		return nil, fmt.Errorf("the triage report lists no group")
	}
	return &rep, nil
}

func stackableGroups(rep *solar.Report) []solar.Group {
	var out []solar.Group
	for _, g := range rep.Groups {
		if g.Stackable {
			out = append(out, g)
		}
	}
	return out
}

// scratchFrames reconstructs the run's frame list from the files left in its scratch directory.
//
// Only the path, the source and the clock are recovered here. Geometry and sharpness are measured
// later and only for the frames a panel actually considers — a few hundred out of eight thousand.
func scratchFrames(scratch string, group solar.Group) ([]solar.Frame, []string) {
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return nil, []string{"sun: phase sequence: rebuild: " + err.Error()}
	}
	clips, warnings := clipsByStem(group)
	var frames []solar.Frame
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), scratchSuffix) {
			continue
		}
		stem, index, ok := splitScratchName(strings.TrimSuffix(e.Name(), scratchSuffix))
		if !ok {
			continue
		}
		clip, ok := clips[stem]
		if !ok || clip.Video == nil || clip.Video.FPS <= 0 {
			continue
		}
		frames = append(frames, solar.Frame{
			Path:   filepath.Join(scratch, e.Name()),
			Source: clip.Path,
			Index:  index,
			TimeMs: clip.Video.CreatedMs + int64(float64(index)*1000/clip.Video.FPS),
		})
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].TimeMs < frames[j].TimeMs })
	return frames, warnings
}

// clipsByStem indexes the group's video members by the stem ingest built their frame names from:
// the file's base name with its extension's dot turned into an underscore.
func clipsByStem(group solar.Group) (map[string]solar.Member, []string) {
	out := map[string]solar.Member{}
	var warnings []string
	for _, m := range group.Members {
		if m.Video == nil {
			continue
		}
		base := filepath.Base(m.Path)
		stem := strings.ReplaceAll(base, ".", "_")
		if m.Video.CreatedMs == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"sun: phase sequence: rebuild: %s carries no creation time, so its frames cannot be dated", base))
			continue
		}
		out[stem] = m
	}
	return out, warnings
}

// splitScratchName cuts a scratch file name into its clip stem and frame index.
func splitScratchName(name string) (stem string, index int, ok bool) {
	cut := strings.LastIndex(name, "_")
	if cut <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(name[cut+1:])
	if err != nil {
		return "", 0, false
	}
	return name[:cut], n, true
}

// hydrateWindow measures the geometry and sharpness of the frames one panel is choosing between.
//
// It is the same measurement ingest made, replayed on demand: FitGeometry for the two circles and
// FrameSharpnessPair for the ranking. A frame whose limb cannot be fitted is dropped rather than
// carried with an empty geometry, which is what the stacker would do with it anyway.
func hydrateWindow(p solar.Preset) hydrator {
	return func(window []solar.Frame) ([]solar.Frame, []string) {
		var out []solar.Frame
		var dropped int
		for _, f := range window {
			if f.Limb.R > 0 {
				out = append(out, f)
				continue
			}
			im, err := fits.ReadImage(f.Path)
			if err != nil {
				dropped++
				continue
			}
			mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
			g, ok := solar.FitGeometry(mono, p.TwoBody)
			if !ok {
				dropped++
				continue
			}
			f.Limb, f.Moon = g.Sun, g.Moon
			f.Score = solar.FrameSharpnessPair(mono, g)
			out = append(out, f)
		}
		var warnings []string
		if dropped > 0 {
			warnings = append(warnings, fmt.Sprintf("%d of %d frames carried no fittable limb", dropped, len(window)))
		}
		return out, warnings
	}
}

// reextractCutClips re-cuts the frames of any clip whose disc does not fit the square ingest gave it.
//
// This is not something frame selection can rescue. The crop is one size for a whole clip, so if the
// Sun is bigger than it, EVERY frame of that clip has the same chords sliced off and the sharpest of
// them is just as square as the rest — which is exactly what the first sheet of the 12 Aug session
// showed on its shallowest panel. The fix has to go back to the video, but only for the clips that
// need it: re-cutting sixteen seconds costs seconds, where the whole session cost sixteen hours.
//
// The new crop is sized from the clip's OWN measured disc rather than from the merged group's, which
// is what went wrong in the first place: a session whose magnification changed between clips has one
// group radius and several real ones.
func reextractCutClips(ctx context.Context, opts Options, group solar.Group, p solar.Preset,
	scratch string, frames []solar.Frame, say func(string)) ([]solar.Frame, []string) {

	var warnings []string
	cut := map[string]bool{}
	for _, m := range group.Members {
		if m.Video == nil || m.Rejected {
			continue
		}
		whole, radius, ok := clipContainment(frames, m.Path, p.TwoBody)
		if !ok || whole >= wholeDiscMin {
			continue
		}
		say(fmt.Sprintf("%s: its disc is cut by the crop (%.1f%% kept) — re-extracting it", filepath.Base(m.Path), whole*100))
		io := p.IngestOpts(scratch, opts.FfmpegBin, radius)
		io.CropMargin = recropMargin
		if _, w, err := solar.IngestVideo(ctx, m.Path, *m.Video, io); err != nil {
			warnings = append(warnings, fmt.Sprintf("sun: phase sequence: re-extract %s: %v", filepath.Base(m.Path), err))
			continue
		} else {
			warnings = append(warnings, prefix("sun: phase sequence: re-extract: ", w)...)
		}
		cut[m.Path] = true
	}
	if len(cut) == 0 {
		return nil, warnings
	}
	again, w := scratchFrames(scratch, group)
	return again, append(warnings, w...)
}

const (
	// wholeDiscMin is how much of the disc a clip must keep before it is left alone.
	wholeDiscMin = 0.995
	// recropMargin is the crop the re-extraction uses, as a fraction of the radius beyond the limb.
	// Generous on purpose: the point is to stop cutting the Sun, and a wider square costs only disk.
	recropMargin = 0.55
)

// clipContainment samples a clip's extracted frames and reports how much of the disc they hold, plus
// the disc radius it measured — the number the re-crop should be sized from.
func clipContainment(frames []solar.Frame, clip string, twoBody bool) (float64, float64, bool) {
	var sample []solar.Frame
	for _, f := range frames {
		if f.Source == clip {
			sample = append(sample, f)
		}
	}
	if len(sample) == 0 {
		return 0, 0, false
	}
	worst, radius := 1.0, 0.0
	n := 0
	for i := 0; i < len(sample) && n < clipContainmentSamples; i += maxInt(1, len(sample)/clipContainmentSamples) {
		f := sample[i]
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			continue
		}
		mono := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{im.Pix[0]}}
		g, ok := solar.FitGeometry(mono, twoBody)
		if !ok || g.Sun.R <= 0 {
			continue
		}
		if w := discInside(mono, g); w < worst {
			worst = w
		}
		if g.Sun.R > radius {
			radius = g.Sun.R
		}
		n++
	}
	return worst, radius, n > 0
}

const clipContainmentSamples = 5
