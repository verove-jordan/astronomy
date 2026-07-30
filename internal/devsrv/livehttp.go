package devsrv

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/sim"
	"github.com/verove-jordan/astronomy/internal/preview"
)

// HTTP surface of the live view. /live/frame deliberately serves the SAME binary 16-bit preview
// format the engine already uses for file previews, so the browser reuses its existing decoder,
// its LUT-based stretch and its zoom/pan viewer instead of growing a second image pipeline.

// defaultLiveIntervalMs is the pause between live exposures. It is a pause, not a frame rate: the
// exposure time dominates, and this only keeps a fast framing loop from pegging the CPU.
const defaultLiveIntervalMs = 100

func (s *Server) liveStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IntervalMs int `json:"interval_ms"`
	}
	_ = decodeBody(w, r, &body)
	interval := time.Duration(body.IntervalMs) * time.Millisecond
	if body.IntervalMs <= 0 {
		interval = defaultLiveIntervalMs * time.Millisecond
	}
	if err := s.live.start(interval); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": true, "interval_ms": interval.Milliseconds()})
}

func (s *Server) liveStop(w http.ResponseWriter, _ *http.Request) {
	s.live.stop()
	writeJSON(w, http.StatusOK, map[string]any{"running": false})
}

// liveDeadlineJSON renders a deadline for the browser, or nil when nothing is in flight — an absent
// field is unambiguous where a zero time would render as year 1.
func liveDeadlineJSON(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// liveFrame serves the newest frame as a downsampled 16-bit preview buffer. `max` bounds the long
// edge; `x,y,w,h` request a full-resolution crop instead, which is what the viewer's 1:1 zoom uses
// to show real sensor pixels rather than an upscaled preview.
func (s *Server) liveFrame(w http.ResponseWriter, r *http.Request) {
	frame, _, seq := s.live.latest()
	if frame == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "no live frame yet", "code": "no_frame"})
		return
	}
	q := r.URL.Query()
	maxEdge := clampInt(atoiOr(q.Get("max"), preview.DefaultMaxEdge), 64, 8192)

	var p *preview.Preview
	if q.Get("w") != "" && q.Get("h") != "" {
		crop := cropRect{
			x: atoiOr(q.Get("x"), 0), y: atoiOr(q.Get("y"), 0),
			w: atoiOr(q.Get("w"), 0), h: atoiOr(q.Get("h"), 0),
		}
		p = previewOfCrop(frame, crop, maxEdge)
	} else {
		p = previewOfFrame(frame, maxEdge)
	}
	if p == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty crop"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Seq", strconv.FormatInt(seq, 10))
	if err := p.Encode(w); err != nil {
		return // the client went away mid-stream; nothing useful to report
	}
}

func (s *Server) liveStats(w http.ResponseWriter, _ *http.Request) {
	frame, stats, seq := s.live.latest()
	expEnds, expUs := s.live.exposureDeadline()
	if frame == nil || stats == nil {
		// Still reported before the first frame: a long first exposure is exactly when a countdown is
		// most wanted, and an empty response would leave the screen looking stuck.
		writeJSON(w, http.StatusOK, map[string]any{
			"running": s.live.isRunning(), "seq": seq,
			"exposure_ends": liveDeadlineJSON(expEnds), "exposure_us": expUs,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": s.live.isRunning(),
		"seq":     seq,
		// When the frame in flight is expected, so the UI can count the wait down instead of showing a
		// static screen for a five-minute sub.
		"exposure_ends": liveDeadlineJSON(expEnds),
		"exposure_us":   expUs,
		"stats":         stats,
	})
}

// liveEvents streams stats as frames land (Server-Sent Events, the same shape the job stream uses,
// so the frontend's existing EventSource handling applies).
func (s *Server) liveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	updates, unsubscribe := s.live.subscribe()
	defer unsubscribe()

	send := func() bool {
		_, stats, seq := s.live.latest()
		if stats == nil {
			return true
		}
		expEnds, expUs := s.live.exposureDeadline()
		payload, err := json.Marshal(map[string]any{
			"seq": seq, "stats": stats,
			"exposure_ends": liveDeadlineJSON(expEnds), "exposure_us": expUs,
		})
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send() // immediate snapshot, so a reconnecting page is not blank until the next frame

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// liveSimulate adjusts the simulated observatory while it runs — defocus the "telescope", change
// the seeing. It is how the focus meter is exercised without a telescope, and how the demo mode
// shows what a bad night looks like.
func (s *Server) liveSimulate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FocusOffsetUm *float64 `json:"focus_offset_um"`
		SeeingArcsec  *float64 `json:"seeing_arcsec"`
		CameraPADeg   *float64 `json:"camera_pa_deg"`
		HotPixels     *int     `json:"hot_pixels"`
		// StarGrid plants N×N evenly spaced stars of the given magnitude across the field, centred
		// on where the telescope is looking. A few arcminutes of real sky can be genuinely empty,
		// so this is how the focus meter gets something to measure on demand.
		StarGrid *struct {
			Count     int     `json:"count"`
			Mag       float64 `json:"mag"`
			SpreadDeg float64 `json:"spread_deg"`
		} `json:"star_grid"`
		// FaintStarsPerDeg2 controls the synthetic faint population. Negative turns it off, which is
		// what a measurement test wants: star_grid exists to give a KNOWN field, and thousands of
		// extra stars would change what the meter is averaging over.
		FaintStarsPerDeg2 *float64 `json:"faint_stars_per_deg2"`
		// FlatPanelADUPerSec puts a flat panel over the aperture; 0 takes it away.
		FlatPanelADUPerSec *float64 `json:"flat_panel_adu_per_sec"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	world := s.world
	s.mu.Unlock()
	if world == nil {
		badRequest(w, "no simulated devices are connected")
		return
	}
	if body.FocusOffsetUm != nil {
		world.SetFocusOffset(*body.FocusOffsetUm)
	}
	if body.SeeingArcsec != nil {
		world.SetSeeing(*body.SeeingArcsec)
	}
	if body.CameraPADeg != nil {
		world.SetCameraAngle(*body.CameraPADeg)
	}
	if body.HotPixels != nil {
		world.SetHotPixels(*body.HotPixels)
	}
	if g := body.StarGrid; g != nil {
		world.SetSyntheticStars(starGrid(world, g.Count, g.Mag, g.SpreadDeg))
	}
	if body.FaintStarsPerDeg2 != nil {
		world.SetFaintStars(*body.FaintStarsPerDeg2)
	}
	if body.FlatPanelADUPerSec != nil {
		world.SetFlatPanel(*body.FlatPanelADUPerSec)
	}
	cfg := world.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"focus_offset_um": cfg.FocusOffsetUm,
		"seeing_arcsec":   cfg.SeeingArcsec,
	})
}

// liveFocusReset clears the focus history (after a filter change, or when starting over).
func (s *Server) liveFocusReset(w http.ResponseWriter, _ *http.Request) {
	s.live.resetFocus()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type cropRect struct{ x, y, w, h int }

// previewOfFrame box-downsamples a frame to at most maxEdge on its long side.
func previewOfFrame(f *device.Frame, maxEdge int) *preview.Preview {
	return previewOfCrop(f, cropRect{x: 0, y: 0, w: f.Width, h: f.Height}, maxEdge)
}

// previewOfCrop downsamples an arbitrary rectangle of the frame. A crop already smaller than
// maxEdge passes through at full resolution, which is what makes 1:1 pixel-peeping honest.
func previewOfCrop(f *device.Frame, c cropRect, maxEdge int) *preview.Preview {
	c.x = clampInt(c.x, 0, maxInt(0, f.Width-1))
	c.y = clampInt(c.y, 0, maxInt(0, f.Height-1))
	if c.w <= 0 || c.w > f.Width-c.x {
		c.w = f.Width - c.x
	}
	if c.h <= 0 || c.h > f.Height-c.y {
		c.h = f.Height - c.y
	}
	if c.w <= 0 || c.h <= 0 {
		return nil
	}
	step := 1
	for (c.w/step) > maxEdge || (c.h/step) > maxEdge {
		step++
	}
	outW, outH := c.w/step, c.h/step
	if outW <= 0 || outH <= 0 {
		return nil
	}
	pix := make([]uint16, outW*outH)
	sample := make([]uint16, 0, outW*outH)
	for y := 0; y < outH; y++ {
		srcY := c.y + y*step
		for x := 0; x < outW; x++ {
			srcX := c.x + x*step
			var sum uint32
			n := uint32(0)
			for dy := 0; dy < step; dy++ {
				row := (srcY + dy) * f.Width
				if srcY+dy >= f.Height {
					break
				}
				for dx := 0; dx < step; dx++ {
					if srcX+dx >= f.Width {
						break
					}
					sum += uint32(f.Pix[row+srcX+dx])
					n++
				}
			}
			v := uint16(0)
			if n > 0 {
				v = uint16(sum / n)
			}
			pix[y*outW+x] = v
			sample = append(sample, v)
		}
	}
	lo, hi := autoStretchUnsorted(sample)
	return &preview.Preview{W: outW, H: outH, C: 1, Pix: pix, AutoLo: lo, AutoHi: hi}
}

// autoStretchUnsorted sorts a copy so the caller's buffer keeps its pixel order.
func autoStretchUnsorted(vals []uint16) (uint16, uint16) {
	cp := append([]uint16(nil), vals...)
	sortUint16(cp)
	return autoStretch(cp)
}

func sortUint16(v []uint16) {
	// A counting sort is O(n) here and avoids a comparison sort over a few hundred thousand samples.
	var counts [65536]uint32
	for _, x := range v {
		counts[x]++
	}
	i := 0
	for value, n := range counts {
		for ; n > 0; n-- {
			v[i] = uint16(value)
			i++
		}
	}
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// starGrid builds an n×n lattice of stars around the current pointing.
func starGrid(world *sim.World, n int, mag, spreadDeg float64) []sim.SyntheticStar {
	if n <= 0 {
		return nil
	}
	if mag == 0 {
		mag = 7
	}
	if spreadDeg <= 0 {
		spreadDeg = 0.2
	}
	ra, dec := world.Pointing()
	out := make([]sim.SyntheticStar, 0, n*n)
	for iy := 0; iy < n; iy++ {
		for ix := 0; ix < n; ix++ {
			fx := (float64(ix) - float64(n-1)/2) / math.Max(1, float64(n-1)/2)
			fy := (float64(iy) - float64(n-1)/2) / math.Max(1, float64(n-1)/2)
			sra, sdec := astro.TangentSky(ra, dec, fx*spreadDeg/2, fy*spreadDeg/2)
			out = append(out, sim.SyntheticStar{RADeg: sra, DecDeg: sdec, Mag: mag})
		}
	}
	return out
}
