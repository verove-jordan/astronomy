package solar

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// triage.go answers the question a real solar folder poses: which of these files can be stacked
// with which? A test session is not a tidy sequence — it is a pile of attempts at different zooms,
// resolutions, exposures and formats, and the engine has to work the grouping out for itself.
//
// The grouping key is the MEASURED disc radius, never the metadata. Metadata is only a label,
// because it lies about scale in both directions: a 48 MP frame at 24 mm and a 12 MP frame at
// 55 mm of digital zoom can land within 2% of the same disc diameter — compatible, but split by any
// rule keyed on dimensions or focal length — while two frames sharing both can differ if the phone
// re-picked a lens.

// defaultScaleTolerance is the largest relative GAP between neighbouring disc radii that still
// counts as the same configuration.
//
// It is deliberately looser than "how well must frames agree to stack", because they do not have to
// agree at all: registration normalises every frame onto a canonical radius anyway, so a scale
// difference inside a group costs a resample we were already doing. What the tolerance really
// separates is *configurations* — the user re-seating the phone on the eyepiece, or picking a
// different zoom — from the natural jitter of re-framing the same setup.
const defaultScaleTolerance = 0.03

// maxGroupSpread caps how far a group may stretch end to end, so a long chain of near-misses cannot
// walk from one configuration into the next. A group wider than this is split at its largest
// internal gap until every part fits.
const maxGroupSpread = 0.08

// defaultMinGroupFrames is the smallest group worth stacking. Below it there is no lucky-imaging
// gain and the per-frame gates have no siblings to judge against.
const defaultMinGroupFrames = 8

// Options tunes triage.
type Options struct {
	ScaleTolerance float64 // relative radius spread within a group; ≤0 → defaultScaleTolerance
	MinGroupFrames int     // ≤0 → defaultMinGroupFrames
	FFmpegBin      string
	Workers        int // ≤0 → 4
}

func (o Options) tolerance() float64 {
	if o.ScaleTolerance > 0 {
		return o.ScaleTolerance
	}
	return defaultScaleTolerance
}

func (o Options) minFrames() int {
	if o.MinGroupFrames > 0 {
		return o.MinGroupFrames
	}
	return defaultMinGroupFrames
}

func (o Options) workers() int {
	if o.Workers > 0 {
		return o.Workers
	}
	return 4
}

// Report is the triage result: every group found, in descending order of how good it looks.
type Report struct {
	Root   string  `json:"root"`
	Files  int     `json:"files"`
	Groups []Group `json:"groups"`
	// Unusable holds the files that never reached a group — unreadable, or with no fittable limb.
	// They are reported rather than dropped: a triage report that quietly omits a third of the
	// folder teaches the user to distrust it.
	Unusable []Member `json:"unusable,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Group is a set of files that share an image scale and can therefore be stacked together.
type Group struct {
	ID    string `json:"id"` // the stable exclusion token
	Label string `json:"label"`
	Kind  Kind   `json:"kind"`

	Files       int     `json:"files"`
	Frames      int     `json:"frames"` // stackable frames (a clip contributes its whole frame count)
	DiscRadius  float64 `json:"disc_radius_px"`
	ArcsecPerPx float64 `json:"arcsec_per_px,omitempty"`
	SpanMs      int64   `json:"span_ms"`
	Detail      float64 `json:"detail"`
	Stackable   bool    `json:"stackable"`
	// NearestPct is the scale gap to the closest other group of the same kind, in percent. It is
	// what tells a user whether merging two groups would cost a 2% resample or a 50% one.
	NearestPct float64  `json:"nearest_pct,omitempty"`
	Members    []Member `json:"members"`
	Notes      []string `json:"notes,omitempty"`
}

// Member is one file inside a group, with the verdict triage reached on it.
type Member struct {
	FrameProbe
	Frames   int      `json:"frames"`
	Rejected bool     `json:"rejected"`
	Reasons  []Reason `json:"reasons,omitempty"`
}

// Reason explains one verdict in terms a user can act on. Rejects distinguishes a disqualification
// from an observation worth showing — a frame exposed differently from its siblings is worth
// mentioning, but it is not a reason to throw it away.
type Reason struct {
	Code    string `json:"code"`
	Text    string `json:"text"`
	Rejects bool   `json:"rejects,omitempty"`
}

// Reason codes.
const (
	ReasonNoLimb      = "no_limb"
	ReasonClipped     = "clipped"
	ReasonEdgeClipped = "disc_clipped_by_frame"
	ReasonTooDark     = "under_exposed"
	ReasonExposure    = "exposure_outlier"
	ReasonDefocused   = "defocused"
	ReasonHaze        = "haze_or_cloud"
	ReasonUnreadable  = "unreadable"
)

// Triage inspects every capture file under root and groups them by measured image scale.
func Triage(ctx context.Context, root string, opts Options) (*Report, error) {
	paths, err := collectSources(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no capture files under %s", root)
	}
	scratch, err := os.MkdirTemp("", "solar-triage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)

	probes, err := probeAll(ctx, paths, scratch, opts)
	if err != nil {
		return nil, err
	}
	rep := &Report{Root: root, Files: len(probes)}
	rep.Groups = groupByScale(probes, opts)
	for i := range rep.Groups {
		gateGroup(&rep.Groups[i], opts)
	}
	annotateNeighbours(rep.Groups)
	sort.SliceStable(rep.Groups, func(i, j int) bool { return groupRank(rep.Groups[i]) > groupRank(rep.Groups[j]) })
	rep.Unusable = unusableMembers(probes)
	rep.Warnings = collectWarnings(probes, rep.Groups)
	return rep, nil
}

// unusableMembers collects the probes that never reached a group, each carrying the reason.
func unusableMembers(probes []FrameProbe) []Member {
	var out []Member
	for _, p := range probes {
		if p.DiscOK && p.Disc.R > 0 {
			continue
		}
		m := Member{FrameProbe: p, Frames: 1}
		applyAbsoluteGates(&m)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// probeAll measures every file concurrently. A file that cannot be probed is still returned, with
// its error recorded — a triage report that silently omits files is worse than useless.
func probeAll(ctx context.Context, paths []string, scratch string, opts Options) ([]FrameProbe, error) {
	out := make([]FrameProbe, len(paths))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.workers())
	var mu sync.Mutex
	for i, path := range paths {
		i, path := i, path
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			var p FrameProbe
			if videoExts[strings.ToLower(filepath.Ext(path))] {
				p = probeVideoFile(gctx, opts.FFmpegBin, path, scratch)
			} else {
				p = probeStill(gctx, path, scratch, readStillMeta(path))
			}
			mu.Lock()
			out[i] = p
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// groupByScale clusters probes of the same kind by measured disc radius.
//
// The split rule is the relative GAP between neighbouring radii, not the distance from a cluster
// anchor. Anchoring chops a genuinely continuous run at an arbitrary point — three frames of one
// setup at 1081, 1096 and 1098 px become two groups purely because the third crossed a threshold
// measured from the first. Gap splitting keeps them together and cuts where the data actually is
// discontinuous, which is where the user changed something.
func groupByScale(probes []FrameProbe, opts Options) []Group {
	byKind := map[Kind][]FrameProbe{}
	for _, p := range probes {
		if !p.DiscOK || p.Disc.R <= 0 {
			continue
		}
		byKind[p.Kind] = append(byKind[p.Kind], p)
	}
	kinds := make([]Kind, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] }) // deterministic output

	var groups []Group
	for _, kind := range kinds {
		list := byKind[kind]
		sort.Slice(list, func(i, j int) bool { return list[i].Disc.R < list[j].Disc.R })
		for _, run := range splitByGap(list, opts.tolerance()) {
			groups = append(groups, newGroup(kind, run))
		}
	}
	return groups
}

// splitByGap cuts a radius-sorted list wherever consecutive radii differ by more than tol, then
// enforces the total-spread cap on each run.
func splitByGap(list []FrameProbe, tol float64) [][]FrameProbe {
	var runs [][]FrameProbe
	start := 0
	for i := 1; i <= len(list); i++ {
		if i < len(list) && list[i].Disc.R <= list[i-1].Disc.R*(1+tol) {
			continue
		}
		runs = append(runs, list[start:i])
		start = i
	}
	var out [][]FrameProbe
	for _, r := range runs {
		out = append(out, capSpread(r)...)
	}
	return out
}

// capSpread packs a run into the fewest contiguous groups that each stay inside maxGroupSpread.
//
// It fills greedily from the small end rather than cutting at the widest internal gap. By the time
// a run reaches here the gap rule has already removed every real discontinuity, so what is left is
// a near-uniform ramp — and on a ramp "widest gap" is decided by noise in the fifth significant
// figure, which peels off one singleton per recursion instead of producing balanced groups.
func capSpread(run []FrameProbe) [][]FrameProbe {
	if len(run) < 2 || run[0].Disc.R <= 0 {
		return [][]FrameProbe{run}
	}
	var out [][]FrameProbe
	start := 0
	for i := 1; i < len(run); i++ {
		if run[i].Disc.R > run[start].Disc.R*(1+maxGroupSpread) {
			out = append(out, run[start:i])
			start = i
		}
	}
	return append(out, run[start:])
}

// newGroup builds a group from its members.
func newGroup(kind Kind, members []FrameProbe) Group {
	g := Group{Kind: kind, Files: len(members)}
	radii := make([]float64, 0, len(members))
	scales := make([]float64, 0, len(members))
	var first, last int64
	for _, p := range members {
		frames := 1
		if p.Video != nil && p.Video.Frames > 0 {
			frames = p.Video.Frames
		}
		g.Frames += frames
		g.Members = append(g.Members, Member{FrameProbe: p, Frames: frames})
		radii = append(radii, p.Disc.R)
		if p.ArcsecPerPx > 0 {
			scales = append(scales, p.ArcsecPerPx)
		}
		if p.TakenAtMs > 0 {
			if first == 0 || p.TakenAtMs < first {
				first = p.TakenAtMs
			}
			if p.TakenAtMs > last {
				last = p.TakenAtMs
			}
		}
	}
	g.DiscRadius = median(radii)
	g.ArcsecPerPx = median(scales)
	g.SpanMs = last - first
	g.ID = fmt.Sprintf("sun-%s-r%d", kind, int(math.Round(g.DiscRadius)))
	g.Label = groupLabel(g, members)
	sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].TakenAtMs < g.Members[j].TakenAtMs })
	return g
}

// groupLabel describes a group the way a user thinks about it — the gear and the zoom — even though
// none of that took part in the grouping decision.
func groupLabel(g Group, members []FrameProbe) string {
	parts := []string{string(g.Kind)}
	if m := members[0].CameraModel; m != "" {
		parts = append(parts, m)
	}
	if fl := commonFocalLengths(members); fl != "" {
		parts = append(parts, fl)
	}
	if w, h := members[0].Width, members[0].Height; w > 0 {
		// Report the long edge first. The same capture reads 8064×6048 through the DNG's EXIF
		// (sensor orientation) and 3024×4032 through Spotlight (display orientation), and showing
		// both forms in one table invites the reader to think they are different configurations.
		if h > w {
			w, h = h, w
		}
		parts = append(parts, fmt.Sprintf("%d×%d", w, h))
	}
	parts = append(parts, fmt.Sprintf("disc ⌀%.0f px", 2*g.DiscRadius))
	return strings.Join(parts, " · ")
}

// commonFocalLengths summarises the 35mm-equivalent focal lengths present in a group. More than one
// value is informative rather than alarming: it is exactly the case where two different zoom
// settings happened to produce the same image scale.
func commonFocalLengths(members []FrameProbe) string {
	seen := map[int]bool{}
	var vals []int
	for _, p := range members {
		if p.FocalLength35mm > 0 && !seen[p.FocalLength35mm] {
			seen[p.FocalLength35mm] = true
			vals = append(vals, p.FocalLength35mm)
		}
	}
	if len(vals) == 0 {
		return ""
	}
	sort.Ints(vals)
	s := make([]string, len(vals))
	for i, v := range vals {
		s[i] = fmt.Sprintf("%dmm", v)
	}
	return strings.Join(s, "/")
}

// groupRank orders groups for the hero pick: usable frames matter, but sharpness decides between
// two groups that both have enough. An unstackable group always sorts last.
func groupRank(g Group) float64 {
	if !g.Stackable {
		return -1
	}
	return g.Detail * math.Log1p(float64(g.Frames))
}

// collectWarnings surfaces anything the user should know that is not attached to a single group.
func collectWarnings(probes []FrameProbe, groups []Group) []string {
	var w []string
	var unreadable, noLimb int
	for _, p := range probes {
		switch {
		case p.Err != "":
			unreadable++
		case !p.DiscOK:
			noLimb++
		}
	}
	if unreadable > 0 {
		w = append(w, fmt.Sprintf("%d file(s) could not be read and were skipped", unreadable))
	}
	if noLimb > 0 {
		w = append(w, fmt.Sprintf("%d file(s) show no solar limb (zoomed past the disc, or clouded out) and cannot be registered", noLimb))
	}
	var stackable int
	for _, g := range groups {
		if g.Stackable {
			stackable++
		}
	}
	if stackable == 0 {
		w = append(w, "no group has enough usable frames to stack")
	}
	return w
}

// collectSources walks root for capture files, skipping hidden directories and anything that is
// neither a still nor a clip.
func collectSources(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if stillExts[ext] || videoExts[ext] {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

var (
	stillExts = map[string]bool{
		".fits": true, ".fit": true, ".fts": true,
		".tif": true, ".tiff": true, ".png": true, ".jpg": true, ".jpeg": true,
		".dng": true, ".heic": true, ".heif": true,
		".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true,
	}
	videoExts = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".m4v": true, ".avi": true, ".ser": true}
)

// MergeGroups combines several scale groups into one, for a run that has opted into rescaling.
//
// It is safe because registration derives each frame's scale from its own fitted limb, so frames of
// different disc sizes land on the canonical raster correctly whatever they started at. What it
// cannot do is create resolution: a group shot at half the disc size contributes frames that are
// upscaled to match, adding signal-to-noise but no detail. Whether that trade is worth taking is a
// judgement about the capture, which is why it is a knob and not a default.
func MergeGroups(groups []Group) Group {
	if len(groups) == 0 {
		return Group{}
	}
	// Anchor on the largest disc: upscaling the smaller groups preserves the best group's detail,
	// where downscaling to the smallest would throw it away.
	best := 0
	for i, g := range groups {
		if g.DiscRadius > groups[best].DiscRadius {
			best = i
		}
	}
	out := Group{
		Kind: groups[best].Kind, DiscRadius: groups[best].DiscRadius,
		ArcsecPerPx: groups[best].ArcsecPerPx, Stackable: true,
	}
	ids := make([]string, 0, len(groups))
	var detail []float64
	for _, g := range groups {
		out.Files += g.Files
		out.Frames += g.Frames
		out.Members = append(out.Members, g.Members...)
		ids = append(ids, g.ID)
		detail = append(detail, g.Detail)
		if g.SpanMs > out.SpanMs {
			out.SpanMs = g.SpanMs
		}
	}
	out.Detail = median(detail)
	out.ID = "sun-merged-" + strings.Join(ids, "+")
	out.Label = fmt.Sprintf("%d groups merged onto ⌀%.0f px (rescaled)", len(groups), 2*out.DiscRadius)
	out.Notes = append(out.Notes, fmt.Sprintf("rescaled from %d scale group(s): %s", len(groups), strings.Join(ids, ", ")))
	sort.Slice(out.Members, func(i, j int) bool { return out.Members[i].TakenAtMs < out.Members[j].TakenAtMs })
	return out
}
