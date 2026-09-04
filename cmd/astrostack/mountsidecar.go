package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// Asking the device server rather than fighting it for the port.
//
// macOS gives a serial port to exactly ONE process, so while `astrostack device` holds the hand
// controller these commands cannot open it. On an observing night that is the NORMAL state, not a
// fault — the sidecar is meant to be running — and "Serial port busy" is a useless thing to say to
// somebody who just wants to read what is stored in their mount.
//
// The sidecar already performs both operations, over HTTP, through the same nexstar.Audit and
// nexstar.Restore this file's callers use (internal/devsrv/mountaudit.go). So the command asks it.
// Opening the port directly stays the path when no device server is running, which is what makes
// these commands work on a bench with nothing else started.

const (
	// sidecarAuditTimeout is generous on purpose: eighty-eight periodic-error bins is eighty-eight
	// round trips on a 9600-baud link.
	sidecarAuditTimeout = 2 * time.Minute
	// sidecarResetTimeout is longer again, because a restore reads back every bin it writes.
	sidecarResetTimeout = 5 * time.Minute
	// sidecarProbeTimeout only has to answer "is anything listening", so a stopped server costs
	// nothing. A stopped one refuses the connection immediately.
	sidecarProbeTimeout = 3 * time.Second
)

// mountSidecar is a running device server that is holding a real mount.
type mountSidecar struct {
	addr  string
	model string
	path  string // the serial port it is holding
}

// findMountSidecar returns the running device server if it is holding a real hand controller, and
// nil otherwise. A stopped sidecar is the ordinary case and must never read as an error.
//
// A SIMULATED mount is deliberately not a reason to route through it: the simulator holds no serial
// port, so the real one is free, and auditing a simulator when a hand controller is plugged in would
// answer a question nobody asked.
func findMountSidecar(ctx context.Context, addr string) *mountSidecar {
	if addr == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sidecarProbeTimeout)
	defer cancel()
	var health struct {
		Connected struct {
			Mount bool `json:"mount"`
		} `json:"connected"`
	}
	if err := sidecarCall(ctx, http.MethodGet, addr, "/health", nil, &health); err != nil {
		return nil
	}
	if !health.Connected.Mount {
		return nil // running, but not holding the port — open it directly
	}
	var st struct {
		Mount struct {
			Name   string `json:"name"`
			Driver string `json:"driver"`
		} `json:"mount"`
		Link struct {
			Path string `json:"path"`
		} `json:"link"`
	}
	if err := sidecarCall(ctx, http.MethodGet, addr, "/mount", nil, &st); err != nil {
		return nil
	}
	if st.Mount.Driver == "sim" {
		return nil
	}
	return &mountSidecar{addr: addr, model: st.Mount.Name, path: st.Link.Path}
}

// describe is the line printed before the report, so it is never ambiguous which process read the
// mount — the numbers are the same either way, but where they came from is not.
func (s *mountSidecar) describe() string {
	where := s.path
	if where == "" {
		where = "the hand controller"
	}
	return fmt.Sprintf("through the running device server on %s, which is holding %s (%s)",
		s.addr, where, s.model)
}

// audit reads back everything stored in the mount, through the process that owns the port.
func (s *mountSidecar) audit(ctx context.Context) (nexstar.AuditReport, error) {
	ctx, cancel := context.WithTimeout(ctx, sidecarAuditTimeout)
	defer cancel()
	var out struct {
		Connected bool                `json:"connected"`
		Audit     nexstar.AuditReport `json:"audit"`
	}
	if err := sidecarCall(ctx, http.MethodGet, s.addr, "/mount/audit", nil, &out); err != nil {
		return nexstar.AuditReport{}, err
	}
	if !out.Connected {
		return nexstar.AuditReport{}, errors.New(
			"the device server let go of the mount while it was being read — reconnect it in Capture > Devices, or stop `astrostack device` and re-run")
	}
	return out.Audit, nil
}

// reset asks the device server to put back what this application can write.
//
// The backup directory is deliberately NOT sent: where the pre-change state is written is the
// server's decision, because the server is the process that must be able to write it. The result
// reports the path it chose.
func (s *mountSidecar) reset(ctx context.Context, opts nexstar.RestoreOptions) (nexstar.RestoreResult, error) {
	ctx, cancel := context.WithTimeout(ctx, sidecarResetTimeout)
	defer cancel()
	body := map[string]any{
		"apply":               !opts.DryRun,
		"pec":                 opts.PEC,
		"pec_playback":        opts.PECPlayback,
		"guide_rate":          opts.GuideRate,
		"guide_rate_fraction": opts.GuideRateFraction,
		"site":                opts.Site,
		"lat_deg":             opts.SiteLatDeg,
		"lon_deg":             opts.SiteLonDeg,
		"clock":               opts.Clock,
		"tracking":            opts.Tracking,
		"tracking_on":         opts.TrackingOn,
		"tracking_rate":       opts.TrackingRate,
	}
	var res nexstar.RestoreResult
	if err := sidecarCall(ctx, http.MethodPost, s.addr, "/mount/reset", body, &res); err != nil {
		return nexstar.RestoreResult{}, err
	}
	return res, nil
}

// sidecarCall performs one request against the device server and decodes the reply, turning the
// server's own JSON error into an error rather than a struct full of zeroes.
func sidecarCall(ctx context.Context, method, addr, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	// The device server answers 4xx/5xx with {"error": …}; surfacing that beats "unexpected status".
	var wrapped struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Error != "" {
		return errors.New(wrapped.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the device server answered %s", resp.Status)
	}
	return json.Unmarshal(raw, out)
}
