package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/planetary"
)

// planetaryAlignPoints estimates how many stacking reference points the first luminance frame of a
// selection supports, to seed the planetary align_points knob. POST /api/planetary/align-points.
func (s *Server) planetaryAlignPoints(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
		MinPx int      `json:"min_px"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	roots, ok := s.resolveRoots(body.Path, body.Paths)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	inv, err := s.scanCache.ScanMany(r.Context(), roots, inspect.DefaultScanOptions())
	if err != nil {
		serverError(w, err)
		return
	}
	src, load, cleanup, ferr := s.estimatorFrame(r.Context(), inv)
	if cleanup != nil {
		defer cleanup()
	}
	if ferr != nil {
		badRequest(w, ferr.Error())
		return
	}
	im, err := planetary.LoadLuminanceFrame(load)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	est := planetary.EstimateAlignPoints(im, body.MinPx)
	est.Frame = src
	writeJSON(w, http.StatusOK, est)
}

// estimatorFrame resolves an inventory to one loadable luminance frame: a still light directly, else
// the first video's first frame extracted into a temp dir under WorkDir (cleanup removes it). SER
// captures are rejected (Siril-convert only). Returns (reportPath, loadablePath, cleanup, error).
func (s *Server) estimatorFrame(ctx context.Context, inv *inspect.Inventory) (string, string, func(), error) {
	if fr, ok := firstLuminanceFrame(inv); ok {
		return fr.Path, fr.Path, nil, nil
	}
	if len(inv.Videos) == 0 {
		return "", "", nil, fmt.Errorf("no light frames or videos found in the selection")
	}
	vid := firstVideo(inv.Videos)
	if strings.ToLower(filepath.Ext(vid)) == ".ser" {
		return "", "", nil, fmt.Errorf("SER captures are not supported by the estimator yet — stack normally, or convert to MP4/AVI")
	}
	if err := os.MkdirAll(s.cfg.WorkDir, 0o755); err != nil {
		return "", "", nil, err
	}
	tmp, err := os.MkdirTemp(s.cfg.WorkDir, "appoints-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	load, err := planetary.ExtractFirstFrame(ctx, s.cfg.FfmpegBin, vid, tmp)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	return vid, load, cleanup, nil
}

// firstLuminanceFrame picks the estimator's frame from the inventory's still lights: the
// chronologically-first light of the preferred filter — "L" when present, else the first canonical
// channel, else the unfiltered/mono group, else any. DateObsMs==0 sorts last; ties break on Path.
func firstLuminanceFrame(inv *inspect.Inventory) (*inspect.Frame, bool) {
	lights := map[string][]*inspect.Frame{}
	for _, fr := range inv.Frames {
		if fr.Type == inspect.Light {
			lights[fr.Filter] = append(lights[fr.Filter], fr)
		}
	}
	if len(lights) == 0 {
		return nil, false
	}
	group := preferredLightGroup(lights)
	sort.SliceStable(group, func(i, j int) bool { return frameEarlier(group[i], group[j]) })
	return group[0], true
}

// preferredLightGroup returns the light group to sample: L, then the first present canonical
// channel, then the mono/unfiltered group, then any group deterministically.
func preferredLightGroup(lights map[string][]*inspect.Frame) []*inspect.Frame {
	for _, f := range planetary.CanonicalFilters {
		if g := lights[f]; len(g) > 0 {
			return g
		}
	}
	if g := lights[""]; len(g) > 0 {
		return g
	}
	keys := make([]string, 0, len(lights))
	for k := range lights {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return lights[keys[0]]
}

// frameEarlier orders frames chronologically, treating a missing timestamp (0) as last, tie-broken by path.
func frameEarlier(a, b *inspect.Frame) bool {
	switch {
	case a.DateObsMs == 0 && b.DateObsMs != 0:
		return false
	case a.DateObsMs != 0 && b.DateObsMs == 0:
		return true
	case a.DateObsMs != b.DateObsMs:
		return a.DateObsMs < b.DateObsMs
	default:
		return a.Path < b.Path
	}
}

// firstVideo picks the estimator's video deterministically (lowest path).
func firstVideo(videos []*inspect.Frame) string {
	best := videos[0].Path
	for _, v := range videos[1:] {
		if v.Path < best {
			best = v.Path
		}
	}
	return best
}
