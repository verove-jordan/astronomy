// Package capture is the engine's half of the capture subsystem: the auto-run sequencer, the
// session bookkeeping, and the typed client onto the device server.
//
// The split matters. The device server (internal/devsrv, its own process) moves hardware; this
// package knows what a session MEANS — which target, which mosaic tile, which night, how many
// frames still owed, where the files belong. Keeping the sequencer here is what lets a session
// survive a device-server restart and lets its progress live in the database with everything else.
package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Client talks to the device server over localhost.
type Client struct {
	base string
	http *http.Client
}

// NewClient builds a device-server client for "host:port".
func NewClient(addr string) *Client {
	return &Client{
		base: "http://" + addr,
		// No global timeout: an exposure can legitimately run for many minutes, and the caller's
		// context is the real bound.
		http: &http.Client{},
	}
}

// Health reports whether the device server is up, and which drivers it has.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var out Health
	err := c.do(ctx, http.MethodGet, "/health", nil, &out)
	return out, err
}

// Health mirrors the device server's /health payload.
type Health struct {
	OK      bool `json:"ok"`
	Drivers []struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Available bool   `json:"available"`
		Detail    string `json:"detail"`
	} `json:"drivers"`
	Connected map[string]bool `json:"connected"`
}

// CameraState is the device server's camera snapshot.
type CameraState struct {
	Connected bool                 `json:"connected"`
	Caps      device.CameraCaps    `json:"caps"`
	Controls  []device.Control     `json:"controls"`
	ROI       device.ROI           `json:"roi"`
	Exposure  device.ExposureState `json:"exposure"`
	Streaming bool                 `json:"streaming"`
}

// Control returns one control by name.
func (s CameraState) Control(name string) (device.Control, bool) {
	for _, ctl := range s.Controls {
		if ctl.Name == name {
			return ctl, true
		}
	}
	return device.Control{}, false
}

func (c *Client) ConnectCamera(ctx context.Context, driver string) (CameraState, error) {
	var out CameraState
	err := c.do(ctx, http.MethodPost, "/camera/connect", map[string]any{"driver": driver}, &out)
	return out, err
}

func (c *Client) Camera(ctx context.Context) (CameraState, error) {
	var out CameraState
	err := c.do(ctx, http.MethodGet, "/camera", nil, &out)
	return out, err
}

func (c *Client) SetControl(ctx context.Context, name string, value int64) error {
	return c.do(ctx, http.MethodPost, "/camera/control",
		map[string]any{"name": name, "value": value}, nil)
}

func (c *Client) StartExposure(ctx context.Context, dark bool) error {
	return c.do(ctx, http.MethodPost, "/camera/expose", map[string]any{"dark": dark}, nil)
}

func (c *Client) AbortExposure(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/camera/abort", nil, nil)
}

// SavedFrame is what the device server reports after writing a frame.
type SavedFrame struct {
	Path       string `json:"path"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	ExposureUs int64  `json:"exposure_us"`
	Gain       int64  `json:"gain"`
	TempMilliC int    `json:"temp_milli_c"`
	StartedAt  string `json:"started_at"`
}

// SaveRequest carries the metadata only the engine knows (target, mosaic tile, session) plus the
// destination path; the device server fills in what it measures.
type SaveRequest struct {
	Path      string  `json:"path"`
	Type      string  `json:"type"`
	Filter    string  `json:"filter"`
	Object    string  `json:"object"`
	Telescope string  `json:"telescope"`
	FocalMM   float64 `json:"focal_mm"`
	RADeg     float64 `json:"ra_deg"`
	DecDeg    float64 `json:"dec_deg"`
	HasCoord  bool    `json:"has_coord"`
	Panel     string  `json:"panel"`
	SessionID string  `json:"session_id"`
	TargetTem float64 `json:"target_temp_c"`
	HasTarget bool    `json:"has_target_temp"`
}

func (c *Client) Save(ctx context.Context, req SaveRequest) (SavedFrame, error) {
	var out SavedFrame
	err := c.do(ctx, http.MethodPost, "/camera/save", req, &out)
	return out, err
}

// WheelState is the device server's wheel snapshot.
type WheelState struct {
	Connected bool              `json:"connected"`
	Wheel     device.WheelState `json:"wheel"`
}

func (c *Client) ConnectWheel(ctx context.Context, driver string, names []string) (WheelState, error) {
	var out WheelState
	err := c.do(ctx, http.MethodPost, "/wheel/connect",
		map[string]any{"driver": driver, "names": names}, &out)
	return out, err
}

func (c *Client) Wheel(ctx context.Context) (WheelState, error) {
	var out WheelState
	err := c.do(ctx, http.MethodGet, "/wheel", nil, &out)
	return out, err
}

// SetFilter moves the wheel and waits for it to settle — exposing through a half-open filter
// silently ruins a sub, so the sequencer always waits.
func (c *Client) SetFilter(ctx context.Context, slot int) (WheelState, error) {
	var out WheelState
	err := c.do(ctx, http.MethodPost, "/wheel/position",
		map[string]any{"slot": slot, "wait": true}, &out)
	return out, err
}

// MountState is the device server's mount snapshot.
type MountState struct {
	Connected bool              `json:"connected"`
	Mount     device.MountState `json:"mount"`
}

func (c *Client) Mount(ctx context.Context) (MountState, error) {
	var out MountState
	err := c.do(ctx, http.MethodGet, "/mount", nil, &out)
	return out, err
}

// Goto and Sync are the centring loop's two mount commands: point at a J2000 target, and tell the
// mount where it really is.
func (c *Client) Goto(ctx context.Context, raDeg, decDeg float64) error {
	return c.do(ctx, http.MethodPost, "/mount/goto",
		map[string]any{"ra_deg": raDeg, "dec_deg": decDeg}, nil)
}

func (c *Client) Sync(ctx context.Context, raDeg, decDeg float64) error {
	return c.do(ctx, http.MethodPost, "/mount/sync",
		map[string]any{"ra_deg": raDeg, "dec_deg": decDeg}, nil)
}

func (c *Client) Nudge(ctx context.Context, raArcsec, decArcsec float64) (MountState, error) {
	var out MountState
	err := c.do(ctx, http.MethodPost, "/mount/nudge",
		map[string]any{"ra_arcsec": raArcsec, "dec_arcsec": decArcsec}, &out)
	return out, err
}

// NudgeResult reports what a measured nudge achieved, in sensor pixels.
type NudgeResult struct {
	Measured bool    `json:"measured"`
	DXPx     float64 `json:"achieved_dx_px"`
	DYPx     float64 `json:"achieved_dy_px"`
	Reason   string  `json:"reason,omitempty"`
}

// NudgeMeasured moves the mount and reports how far the stars ACTUALLY went, by watching one across
// the move. A commanded dither and an achieved one differ by whatever backlash the gears take up, and
// the dither planner needs the second number to keep its spread honest.
//
// Measured is false when there was no usable star; the move still happened, so the caller carries on
// with an unmeasured dither rather than losing the frame.
func (c *Client) NudgeMeasured(ctx context.Context, raArcsec, decArcsec, exposureSec float64) (NudgeResult, error) {
	var out NudgeResult
	err := c.do(ctx, http.MethodPost, "/mount/nudge", map[string]any{
		"ra_arcsec": raArcsec, "dec_arcsec": decArcsec,
		"measure": true, "measure_exposure_sec": exposureSec,
	}, &out)
	return out, err
}

// ExposureStatus polls the current still-exposure state.
func (c *Client) ExposureStatus(ctx context.Context) (device.ExposureState, error) {
	st, err := c.Camera(ctx)
	if err != nil {
		return device.ExposureIdle, err
	}
	return st.Exposure, nil
}

// Error is a device-server error with its machine-readable code, so the sequencer can tell "busy"
// (retry) from "not connected" (stop and tell the user).
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("device server: %s (%s)", e.Message, e.Code)
	}
	return "device server: " + e.Message
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("device server unreachable at %s: %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// waitStep is how often the sequencer polls a running exposure. Short enough that a 32 µs framing
// shot feels instant, long enough that a 10-minute sub costs almost no traffic.
const waitStep = 200 * time.Millisecond
