package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/capture"
	"github.com/verove-jordan/astronomy/internal/skylog"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Finishing a night from the observation journal.
//
// Sessions end early — cloud, a cable, dawn, a mount that lost its USB adapter. Until now the only
// answer was to retype the whole plan and remember which channels were short, which is how a night
// gets shot twice in L and never in Hα. Resume reads what the session actually recorded, subtracts
// it, and starts the remainder with the SAME request: same folder, same focal length, same pointing,
// same dither settings. Only the counts change.
//
// It deliberately creates a NEW session row rather than reopening the old one. The old night
// happened — it has its own conditions, its own weather, its own frames — and rewriting it to look
// like it ran until morning would be a lie the logbook then repeats forever. The link is kept in the
// new session's request as resumed_from.

// resumeCaptureSession starts the frames a session still owes. POST /api/capture/sessions/{id}/resume
func (s *Server) resumeCaptureSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	row, err := s.store.GetCaptureSession(r.Context(), id)
	if err != nil {
		badRequest(w, "unknown session")
		return
	}
	req, warning, err := s.requestFromSession(row)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	stats, err := s.store.CaptureFrameStats(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}

	remaining := capture.Remaining(req.Sequence, framesTallied(stats))
	if remaining.TotalFrames() == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("session %d has every frame it planned; there is nothing to resume", id),
			"code":  "nothing_to_resume",
		})
		return
	}
	req.Sequence = remaining
	req.ResumedFrom = id

	// The root is re-validated rather than trusted: it was checked when the session started, and the
	// allowed locations can have changed since — a drive unmounted, a data directory moved.
	root, err := s.captureRoot(req.Root)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	req.Root = root

	extra := map[string]any{"resumed_from": id, "remaining_frames": remaining.TotalFrames()}
	if warning != "" {
		extra["warning"] = warning
	}
	s.launchCapture(w, r, req, s.sessionSite(row), sessionTarget(row, req), extra)
}

// requestFromSession rebuilds what the session was started with.
//
// Sessions created before the request column exists carry only their columns, so the rest is taken
// from this machine's configuration — which is a guess, and is reported as one. A resumed run whose
// focal length silently came from somewhere else would produce frames that do not stack with the
// ones beside them, and the observer has to be told rather than discover it later.
func (s *Server) requestFromSession(row store.CaptureSession) (capture.Request, string, error) {
	var req capture.Request
	if len(row.Request) > 0 {
		if err := json.Unmarshal(row.Request, &req); err != nil {
			return capture.Request{}, "", fmt.Errorf("this session's request cannot be read: %v", err)
		}
	}
	warning := ""
	if len(req.Sequence.Steps) == 0 {
		// Pre-0022, or a request that never recorded one: fall back to the sequence column.
		if len(row.Sequence) == 0 {
			return capture.Request{}, "", fmt.Errorf("this session recorded no sequence, so there is nothing to finish")
		}
		if err := json.Unmarshal(row.Sequence, &req.Sequence); err != nil {
			return capture.Request{}, "", fmt.Errorf("this session's sequence cannot be read: %v", err)
		}
		warning = "this session predates full request recording, so the telescope, focal length, " +
			"pointing and dither radius come from the current configuration rather than from the night itself"
	}

	// The columns always win for the things they own: they are what the logbook shows, and a request
	// blob that disagreed with them would put the frames somewhere the journal does not point.
	req.Object, req.Root, req.Panel = row.Object, row.Root, row.Panel
	req.MosaicPlanID = row.MosaicPlanID
	if row.TileIndex >= 0 {
		tile := row.TileIndex
		req.TileIndex = &tile
	} else {
		req.TileIndex = nil
	}
	req.LatDeg, req.LonDeg, req.ElevationM = row.SiteLat, row.SiteLon, row.SiteElevationM
	return req, warning, nil
}

// framesTallied turns the database's per-filter aggregate into what Remaining subtracts.
func framesTallied(stats []store.CaptureFrameStat) []capture.FrameTally {
	out := make([]capture.FrameTally, 0, len(stats))
	for _, st := range stats {
		out = append(out, capture.FrameTally{
			Filter: st.Filter, Type: st.FrameType, Count: st.Frames,
		})
	}
	return out
}

// sessionSite is where the telescope stood, so the resumed run's conditions are logged against the
// same place. A session with no site recorded falls back to the configured one.
func (s *Server) sessionSite(row store.CaptureSession) skylog.Site {
	if row.SiteLat != 0 || row.SiteLon != 0 {
		return skylog.Site{Lat: row.SiteLat, Lon: row.SiteLon, ElevationM: row.SiteElevationM}
	}
	return skylog.Site{Lat: s.cfg.LatDeg, Lon: s.cfg.LonDeg}
}

// sessionTarget is where the resumed run points, taken from the request the night was started with.
// A session that carried no coordinates stays invalid rather than becoming a target at 0,0 — which
// is a real place in Cetus and would make the conditions log lie about the altitude it observed at.
func sessionTarget(_ store.CaptureSession, req capture.Request) skylog.Target {
	if req.RADeg == 0 && req.DecDeg == 0 {
		return skylog.Target{}
	}
	return skylog.Target{RADeg: req.RADeg, DecDeg: req.DecDeg, Valid: true}
}
