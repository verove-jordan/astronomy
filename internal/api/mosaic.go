package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/mosaicplan"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// Mosaic planning endpoints: POST /api/mosaic/preview computes a tile grid statelessly (the
// planner UI calls it debounced on every knob change); GET /api/sky/starfield serves deepstars
// cutouts for the expected-frame previews. Plan persistence lives in mosaicplans.go.

// mosaicRequestBody is the planner input: an object by catalogue name and/or explicit values, the
// rig, and the grid knobs. Pointer fields distinguish "absent" (fall back to catalogue/config)
// from an explicit zero. Shared by preview and plan create/update.
type mosaicRequestBody struct {
	TargetName      string   `json:"target_name"`
	RADeg           *float64 `json:"ra_deg"`
	DecDeg          *float64 `json:"dec_deg"`
	SizeArcmin      *float64 `json:"size_arcmin"`
	SizeMinorArcmin *float64 `json:"size_minor_arcmin"`
	ObjectPADeg     *float64 `json:"object_pa_deg"`

	// Hand-framing: where the GRID sits, when the user has dragged the mosaic off the object.
	// Both must be present to take effect.
	CenterRADeg  *float64 `json:"center_ra_deg"`
	CenterDecDeg *float64 `json:"center_dec_deg"`

	Optics mosaicOpticsBody `json:"optics"`

	OverlapFrac  float64  `json:"overlap_frac"`
	MarginArcmin *float64 `json:"margin_arcmin"`
	CameraPADeg  float64  `json:"camera_pa_deg"`
	RowsOverride int      `json:"rows_override"`
	ColsOverride int      `json:"cols_override"`

	Lat *float64 `json:"lat"`
	Lon *float64 `json:"lon"`
	At  string   `json:"at"` // RFC3339; empty = now
}

// mosaicOpticsBody mirrors the /api/sky equipment echo naming; zero fields fall back to the
// configured rig, so a body without optics plans for the default telescope+camera.
type mosaicOpticsBody struct {
	FocalMM    float64 `json:"focal_mm"`
	ApertureMM float64 `json:"aperture_mm"`
	PixelUm    float64 `json:"pixel_um"`
	SensorWpx  int     `json:"sensor_w_px"`
	SensorHpx  int     `json:"sensor_h_px"`
	BarlowX    float64 `json:"barlow_x"`
	ReducerX   float64 `json:"reducer_x"`
}

// mosaicQueryEcho echoes the fully-resolved inputs so the UI can display what was actually planned.
type mosaicQueryEcho struct {
	Target             string   `json:"target,omitempty"`
	RADeg              float64  `json:"ra_deg"`
	DecDeg             float64  `json:"dec_deg"`
	SizeArcmin         float64  `json:"size_arcmin"`
	SizeMinorArcmin    float64  `json:"size_minor_arcmin,omitempty"`
	ObjectPADeg        *float64 `json:"object_pa_deg,omitempty"`
	CenterRADeg        float64  `json:"center_ra_deg"` // effective grid centre (object unless dragged)
	CenterDecDeg       float64  `json:"center_dec_deg"`
	CenterMoved        bool     `json:"center_moved,omitempty"`
	FovWDeg            float64  `json:"fov_w_deg"`
	FovHDeg            float64  `json:"fov_h_deg"`
	ImageScaleArcsecPx float64  `json:"image_scale_arcsec_px"`
	MarginArcmin       float64  `json:"margin_arcmin"`
	Lat                float64  `json:"lat"`
	Lon                float64  `json:"lon"`
	AtUTCMs            int64    `json:"at_utc_ms"`
}

// mosaicPreview computes a tile plan without persisting anything. POST /api/mosaic/preview
func (s *Server) mosaicPreview(w http.ResponseWriter, r *http.Request) {
	var b mosaicRequestBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	req, echo, err := s.resolveMosaicRequest(b)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	plan, err := mosaicplan.Compute(req)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":    echo,
		"grid":     plan.Grid,
		"tiles":    plan.Tiles,
		"warnings": plan.Warnings,
	})
}

// resolveMosaicRequest turns the wire body into a full mosaicplan.Request: catalogue lookup for
// the named target (explicit values always win), configured rig/site defaults for absent fields.
func (s *Server) resolveMosaicRequest(b mosaicRequestBody) (mosaicplan.Request, mosaicQueryEcho, error) {
	req := mosaicplan.Request{
		OverlapFrac:  b.OverlapFrac,
		MarginArcmin: -1, // package default (10′) unless the body sets one
		CameraPADeg:  b.CameraPADeg,
		RowsOverride: b.RowsOverride,
		ColsOverride: b.ColsOverride,
		Lat:          floatOr(b.Lat, s.cfg.LatDeg),
		Lon:          floatOr(b.Lon, s.cfg.LonDeg),
		Optics: skyplan.Optics{
			FocalMM:    orFloat(b.Optics.FocalMM, s.cfg.FocalLenMM),
			ApertureMM: orFloat(b.Optics.ApertureMM, s.cfg.ApertureMM),
			PixelUm:    orFloat(b.Optics.PixelUm, s.cfg.PixelSizeUm),
			SensorWpx:  orInt(b.Optics.SensorWpx, s.cfg.SensorWpx),
			SensorHpx:  orInt(b.Optics.SensorHpx, s.cfg.SensorHpx),
			// Like every other optics field, an unsent multiplier falls back to the configured rig —
			// without this a body that omits them silently plans the grid at the bare focal length.
			BarlowX:  orFloat(b.Optics.BarlowX, s.cfg.BarlowX),
			ReducerX: orFloat(b.Optics.ReducerX, s.cfg.ReducerX),
		},
	}
	if b.MarginArcmin != nil {
		req.MarginArcmin = *b.MarginArcmin
	}
	if b.At != "" {
		at, err := time.Parse(time.RFC3339, b.At)
		if err != nil {
			return req, mosaicQueryEcho{}, fmt.Errorf("invalid 'at' (want RFC3339)")
		}
		req.At = at.UTC()
	}
	if err := s.resolveMosaicTarget(b, &req); err != nil {
		return req, mosaicQueryEcho{}, err
	}
	return req, s.mosaicEchoOf(strings.TrimSpace(b.TargetName), req), nil
}

// resolveMosaicTarget fills the object half of the request: catalogue values for a named target
// first, explicit body values always winning over them.
func (s *Server) resolveMosaicTarget(b mosaicRequestBody, req *mosaicplan.Request) error {
	name := strings.TrimSpace(b.TargetName)
	if name != "" {
		if rec, ok := skycat.Load(s.cfg.SirilCatalogDir).Lookup(name); ok {
			req.RADeg, req.DecDeg = rec.RADeg, rec.DecDeg
			if rec.HasDiameter {
				req.SizeArcmin = rec.DiameterArcmin
			}
			if rec.HasMinorAxis {
				req.SizeMinorArcmin = rec.MinorAxisArcmin
			}
			if rec.HasPositionAngle {
				req.ObjectPADeg, req.HasObjectPA = rec.PositionAngleDeg, true
			}
		} else if b.RADeg == nil || b.DecDeg == nil {
			return fmt.Errorf("unknown target %q", name)
		}
	} else if b.RADeg == nil || b.DecDeg == nil {
		return fmt.Errorf("target_name or ra_deg+dec_deg required")
	}
	if b.RADeg != nil && b.DecDeg != nil {
		req.RADeg, req.DecDeg = *b.RADeg, *b.DecDeg
	}
	if b.SizeArcmin != nil {
		req.SizeArcmin = *b.SizeArcmin
	}
	if b.SizeMinorArcmin != nil {
		req.SizeMinorArcmin = *b.SizeMinorArcmin
	}
	if b.ObjectPADeg != nil {
		req.ObjectPADeg, req.HasObjectPA = *b.ObjectPADeg, true
	}
	if b.CenterRADeg != nil && b.CenterDecDeg != nil {
		req.CenterRADeg, req.CenterDecDeg, req.HasCenter = *b.CenterRADeg, *b.CenterDecDeg, true
		if req.CenterDecDeg < -90 || req.CenterDecDeg > 90 {
			return fmt.Errorf("center_dec_deg out of range")
		}
	}
	if req.DecDeg < -90 || req.DecDeg > 90 {
		return fmt.Errorf("dec_deg out of range")
	}
	return nil
}

func (s *Server) mosaicEchoOf(name string, req mosaicplan.Request) mosaicQueryEcho {
	fovW, fovH := req.Optics.FOV()
	echo := mosaicQueryEcho{
		Target: name,
		RADeg:  req.RADeg, DecDeg: req.DecDeg,
		SizeArcmin: req.SizeArcmin, SizeMinorArcmin: req.SizeMinorArcmin,
		FovWDeg: round(fovW, 3), FovHDeg: round(fovH, 3),
		ImageScaleArcsecPx: round(req.Optics.ImageScale(), 2),
		MarginArcmin:       req.MarginArcmin,
		Lat:                req.Lat, Lon: req.Lon,
		AtUTCMs: req.At.UnixMilli(),
	}
	if req.MarginArcmin < 0 {
		echo.MarginArcmin = 10
	}
	if req.HasObjectPA {
		pa := req.ObjectPADeg
		echo.ObjectPADeg = &pa
	}
	if req.At.IsZero() {
		echo.AtUTCMs = time.Now().UTC().UnixMilli()
	}
	echo.CenterRADeg, echo.CenterDecDeg = mosaicplan.GridCenter(req)
	echo.CenterMoved = req.HasCenter
	return echo
}

// skyStarfield serves a deepstars cutout (mag ≤ 9) around a center, for the capture assistant's
// expected-frame previews. GET /api/sky/starfield?ra=&dec=&fov=&maxmag=&limit=
func (s *Server) skyStarfield(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ra := floatParam(q, "ra", -1)
	dec := floatParam(q, "dec", 91)
	fov := floatParam(q, "fov", 0)
	maxMag := floatParam(q, "maxmag", 10)
	limit := intParam(q, "limit", 500)
	if fov <= 0 || fov > 30 {
		badRequest(w, "fov (degrees, 0<fov<=30) is required")
		return
	}
	if ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
		badRequest(w, "ra/dec out of range")
		return
	}
	if limit < 1 || limit > 1000 {
		limit = 500
	}

	type starJSON struct {
		RADeg  float64 `json:"ra_deg"`
		DecDeg float64 `json:"dec_deg"`
		Mag    float64 `json:"mag"`
	}
	// 0.75·fov covers the corners of a square canvas drawn fov wide. The catalogue is sorted by
	// magnitude, so cutting at maxmag/limit keeps the brightest stars.
	stars := deepstars.InField(ra, dec, fov*0.75, 0, time.Now().UTC())
	out := make([]starJSON, 0, minIntAPI(len(stars), limit))
	for _, st := range stars {
		if st.Mag > maxMag {
			break
		}
		out = append(out, starJSON{RADeg: st.RADeg, DecDeg: st.DecDeg, Mag: st.Mag})
		if len(out) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stars": out, "count": len(out)})
}

func floatOr(v *float64, def float64) float64 {
	if v != nil {
		return *v
	}
	return def
}

func orFloat(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

func orInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func minIntAPI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
