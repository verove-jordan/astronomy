package graxpert

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RemoteRequest is the JSON body the engine POSTs to the host GraXpert service (cmd/graxpert-host). Only
// paths + flags travel: the engine and the host share the same absolute file paths (the compose bind
// mounts), so the image bytes never cross the wire. Exported so cmd/graxpert-host shares the wire type.
type RemoteRequest struct {
	Op       string  `json:"op"` // OpDenoise | OpBackground
	In       string  `json:"in"`
	Out      string  `json:"out"`
	GPU      bool    `json:"gpu"`
	Batch    int     `json:"batch,omitempty"`
	Strength float64 `json:"strength,omitempty"`
}

// ResultPrefix marks the final line of a /run stream: "<prefix>ok" on success, "<prefix>error:<msg>"
// otherwise. GraXpert's own log lines never start with it, so it unambiguously terminates the stream
// (a plain EOF without it means the service died mid-run).
const ResultPrefix = "__GRAXPERT_RESULT__:"

// remotePing checks the host service is reachable (used by Available/Healthy in offload mode). Short
// timeout: /health is a cheap binary check on the host, not a deep probe.
func (r *Runner) remotePing(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("host GraXpert service %s unreachable (run `just run-graxpert-service`): %w", r.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("host GraXpert service unhealthy: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

// runRemote POSTs one operation to the host service and streams its GraXpert output back through the same
// onProgress contract as a local run, so the job log looks identical whether GraXpert ran locally or on
// the host. The passed ctx bounds the whole call (a denoise can take many minutes) — there is no client
// timeout, matching the local exec path.
func (r *Runner) runRemote(ctx context.Context, rr RemoteRequest, onProgress func(Progress)) error {
	body, err := json.Marshal(rr)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/run", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("host GraXpert %s: %w", r.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("host GraXpert (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var result error
	gotResult := false
	for scanner.Scan() {
		line := scanner.Text()
		if payload, ok := strings.CutPrefix(line, ResultPrefix); ok {
			gotResult = true
			if payload != "ok" {
				result = fmt.Errorf("host GraXpert: %s", strings.TrimPrefix(payload, "error:"))
			}
			continue
		}
		if onProgress != nil {
			onProgress(Progress{Line: line, Percent: parsePercent(line)})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("host GraXpert stream: %w", err)
	}
	if !gotResult {
		return fmt.Errorf("host GraXpert: connection closed before the run completed")
	}
	return result
}
