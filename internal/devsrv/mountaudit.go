package devsrv

import (
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// What is stored in the mount, and how to put back the parts of it this app can write.
//
// These are the HTTP face of nexstar.Audit / nexstar.Restore. Nothing is decided here: the driver
// package owns what the numbers mean, and this owns the two things a server owns — who is allowed to
// ask, and keeping the port free while the answer is being read.
//
// That second one matters more than it looks. Reading eighty-eight periodic-error bins is eighty-eight
// round trips on a 9600-baud link, about two seconds; a restore that rewrites them is twice that
// because every bin is read back. The heartbeat is suspended for the duration for the same reason
// mountlink.go gives — a run that pauses between commands would otherwise look idle at exactly the
// wrong moment and have a ping inserted into the middle of its conversation.

// mountAudit reads back every setting the protocol can reach. GET /mount/audit
//
// It answers 200 with connected:false rather than an error when nothing is attached, matching
// mountStatus: the panel needs to render the absence, and a failed request would blank it instead.
func (s *Server) mountAudit(w http.ResponseWriter, r *http.Request) {
	mount := s.currentMount()
	if mount == nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	defer s.link.Suspend()()

	report, err := nexstar.Audit(r.Context(), mount)
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "audit": report})
}

// mountReset puts back what this application can have written. POST /mount/reset
//
// A dry run unless `apply` is set. That default is deliberate on a route a browser can reach: this is
// the only endpoint in the process that changes state inside a piece of hardware and outlives the
// session, and a mis-click should cost a report rather than an hour of somebody's PEC recording.
func (s *Server) mountReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Apply bool `json:"apply"`

		PEC         bool `json:"pec"`
		PECPlayback bool `json:"pec_playback"`
		GuideRate   bool `json:"guide_rate"`
		Site        bool `json:"site"`
		Clock       bool `json:"clock"`
		Tracking    bool `json:"tracking"`

		// GuideRateFraction, the site and the tracking mode are optional overrides. Left out, the
		// server's own configuration and the driver's documented defaults apply — which is what the UI
		// sends, because the observing site the user edits lives in the browser.
		GuideRateFraction float64  `json:"guide_rate_fraction"`
		LatDeg            *float64 `json:"lat_deg"`
		LonDeg            *float64 `json:"lon_deg"`
		TrackingOn        *bool    `json:"tracking_on"`
		TrackingRate      string   `json:"tracking_rate"`
		Zone              string   `json:"zone"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}

	opts := nexstar.RestoreOptions{
		PEC:               body.PEC,
		PECPlayback:       body.PECPlayback,
		GuideRate:         body.GuideRate,
		GuideRateFraction: body.GuideRateFraction,
		Site:              body.Site,
		Clock:             body.Clock,
		Tracking:          body.Tracking,
		TrackingOn:        true,
		TrackingRate:      body.TrackingRate,
		BackupDir:         s.mountBackupDir(),
		DryRun:            !body.Apply,
	}
	if s.cfg != nil {
		opts.SiteLatDeg, opts.SiteLonDeg = s.cfg.LatDeg, s.cfg.LonDeg
	}
	if body.LatDeg != nil {
		opts.SiteLatDeg = *body.LatDeg
	}
	if body.LonDeg != nil {
		opts.SiteLonDeg = *body.LonDeg
	}
	if body.TrackingOn != nil {
		opts.TrackingOn = *body.TrackingOn
	}
	if body.Zone != "" {
		loc, err := time.LoadLocation(body.Zone)
		if err != nil {
			badRequest(w, "unknown time zone "+body.Zone)
			return
		}
		opts.ClockTime = time.Now().In(loc)
	}

	defer s.link.Suspend()()

	res, err := nexstar.Restore(r.Context(), mount, opts)
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// mountBackupDir is where the pre-change state is saved. The output directory rather than the work
// directory: work is scratch that gets cleared, and this is the only copy of the mount's own table
// that will exist anywhere.
func (s *Server) mountBackupDir() string {
	if s.cfg == nil || s.cfg.OutputDir == "" {
		return "output"
	}
	return s.cfg.OutputDir
}
