package mosaic

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// PanelSource selects how SegmentPanels detects the mosaic's pointings.
type PanelSource string

const (
	SourceAuto    PanelSource = "auto" // folders first, else coords
	SourceFolders PanelSource = "folders"
	SourceCoords  PanelSource = "coords"
)

// Panel is one detected pointing: the LIGHT frame paths that belong to it, its label (plan tile
// folder when matched, else "p01"-style by discovery order), and its approximate center when known.
type Panel struct {
	Label     string
	Paths     map[string]bool // light-frame paths in this panel
	RA, Dec   float64
	HasCenter bool
	PlanTile  *Tile
}

// panelFolderRe matches one path segment naming a panel folder: p01, panel-2, tile_3, pa, …
var panelFolderRe = regexp.MustCompile(`(?i)^(p|panel[-_ ]?|tile[-_ ]?)(\d{1,2}|[a-h])$`)

// SegmentPanels splits the lights of a scan into panels: (1) a panel-folder path segment below
// root, (2) greedy unit-sphere centroid clustering of the OBJCTRA/OBJCTDEC headers with join
// threshold 0.35×fovDeg, (3) no signal → one panel with every light. A non-nil plan matches panels
// to tiles by angular distance < 0.5×fovDeg (stable labels, leftover-tile warnings). Never errors
// on content: structural problems come back as warnings and the best-effort segmentation stands.
func SegmentPanels(frames []inspect.Frame, root string, source PanelSource, fovDeg float64, plan *Plan) ([]Panel, []string) {
	lights := make([]inspect.Frame, 0, len(frames))
	for _, fr := range frames {
		if fr.Type == inspect.Light {
			lights = append(lights, fr)
		}
	}
	if len(lights) == 0 {
		return nil, []string{"no light frames to segment into panels"}
	}
	panels, warns := segmentLights(lights, root, source, fovDeg)
	warns = append(warns, matchPlan(panels, plan, fovDeg)...)
	orderAndLabel(panels)
	return panels, warns
}

// segmentLights dispatches on the requested source. Folder mode falls through to coordinate
// clustering when the layout is absent or partial, so "auto" and "folders" share one path; an
// unknown source degrades to auto with a warning (content problems never error).
func segmentLights(lights []inspect.Frame, root string, source PanelSource, fovDeg float64) ([]Panel, []string) {
	switch source {
	case SourceCoords:
		return segmentCoords(lights, fovDeg)
	case SourceAuto, SourceFolders, "":
		return segmentFolders(lights, root, fovDeg)
	default:
		panels, warns := segmentFolders(lights, root, fovDeg)
		return panels, append([]string{fmt.Sprintf("unknown panel source %q — using auto", string(source))}, warns...)
	}
}

// segmentFolders groups lights by the first panel-folder segment below root. A partial layout
// (some lights outside any panel folder) is treated as NO folder layout: ALL lights fall through
// to coordinate clustering, with a warning saying so.
func segmentFolders(lights []inspect.Frame, root string, fovDeg float64) ([]Panel, []string) {
	byKey := make(map[string][]inspect.Frame)
	orphans := 0
	for _, fr := range lights {
		key, ok := panelFolderKey(root, fr.Path)
		if !ok {
			orphans++
			continue
		}
		byKey[key] = append(byKey[key], fr)
	}
	if len(byKey) == 0 {
		panels, warns := segmentCoords(lights, fovDeg)
		return panels, append([]string{"no panel folders (p01/, panel-2/, tile_3/, …) found — clustering lights by pointing headers"}, warns...)
	}
	if orphans > 0 {
		panels, warns := segmentCoords(lights, fovDeg)
		w := fmt.Sprintf("%d light frame(s) sit outside any panel folder — ignoring the partial folder layout and clustering ALL lights by pointing headers", orphans)
		return panels, append([]string{w}, warns...)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := folderOrder(keys[i]), folderOrder(keys[j])
		if oi != oj {
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	panels := make([]Panel, 0, len(keys))
	for _, k := range keys {
		panels = append(panels, panelOf(byKey[k]))
	}
	return panels, nil
}

// panelFolderKey finds the topmost panel-folder segment of path below root (the file name itself
// never counts). The key is the lowercased segment, so P01/ and p01/ group together.
func panelFolderKey(root, path string) (string, bool) {
	rel := path
	if root != "" {
		if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	dir := filepath.Dir(rel)
	if dir == "." || dir == string(filepath.Separator) {
		return "", false
	}
	for _, seg := range strings.Split(dir, string(filepath.Separator)) {
		if panelFolderRe.MatchString(seg) {
			return strings.ToLower(seg), true
		}
	}
	return "", false
}

// folderOrder ranks a panel-folder key for stable ordering: digits by value, letters a–h as 1–8.
func folderOrder(key string) int {
	m := panelFolderRe.FindStringSubmatch(key)
	if m == nil {
		return 1 << 20
	}
	if n, err := strconv.Atoi(m[2]); err == nil {
		return n
	}
	return int(strings.ToLower(m[2])[0]-'a') + 1
}

// panelOf builds a Panel over member frames: their paths plus the unit-vector centroid of the
// members that carry parseable pointing headers (HasCenter=false when none do).
func panelOf(members []inspect.Frame) Panel {
	p := Panel{Paths: make(map[string]bool, len(members))}
	var sx, sy, sz float64
	known := 0
	for _, fr := range members {
		p.Paths[fr.Path] = true
		ra, dec, ok := frameCoords(fr)
		if !ok {
			continue
		}
		v := raDecVec(ra, dec)
		sx, sy, sz = sx+v[0], sy+v[1], sz+v[2]
		known++
	}
	if known > 0 {
		if ra, dec, ok := vecRADec(sx, sy, sz); ok {
			p.RA, p.Dec, p.HasCenter = ra, dec, true
		}
	}
	return p
}
