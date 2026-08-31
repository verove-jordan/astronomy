package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/job"
)

// registerJobTools wires the task tools: reading the job queue/history and acting on runs
// (cancel/restart/AI-refine, and starting a new processing run). The action tools are Mutating.
func registerJobTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "list_jobs", Category: "tasks",
		Description: "List recent processing tasks (newest first), optionally filtered by status.",
		Schema: objectSchema(nil, map[string]any{
			"status": strProp("filter: queued|running|succeeded|failed|cancelled (omit = all)"),
			"limit":  intProp("max rows (default 20)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return listJobs(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "get_job", Category: "tasks",
		Description: "Get one task's detail: status, step, error, its parameters and its result (object, outputs, warnings, iteration count).",
		Schema:      objectSchema([]string{"id"}, map[string]any{"id": intProp("the job id")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return getJob(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "get_job_iterations", Category: "tasks",
		Description: "Get the AI-supervised finish iterations of a run: per pass the tier, scores, diagnosed defects, reasoning and which was chosen.",
		Schema:      objectSchema([]string{"id"}, map[string]any{"id": intProp("the job id")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return getJobIterations(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "cancel_job", Category: "tasks", Mutating: true,
		Description: "Cancel a running or queued task.",
		Schema:      objectSchema([]string{"id"}, map[string]any{"id": intProp("the job id")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return cancelJob(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "restart_job", Category: "tasks", Mutating: true,
		Description: "Re-run a finished (failed/cancelled/succeeded) task from scratch with the same parameters.",
		Schema:      objectSchema([]string{"id"}, map[string]any{"id": intProp("the job id")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return restartJob(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "refine_job", Category: "tasks", Mutating: true,
		Description: "Retry-improve a completed run: re-finish it under the AI supervisor. tier A=composite look, B=reprocess finish (stretch/colour/background/denoise/star-reduce), C=re-stack from raws (structural fixes). Use B for gradient/colour/noise fixes.",
		Schema: objectSchema([]string{"id"}, map[string]any{
			"id":        intProp("the source job id"),
			"tier":      strProp("A|B|C ceiling (default B)"),
			"max_iters": intProp("max supervisor passes (0 = default)"),
			"params":    map[string]any{"type": "object", "description": "fine knob overrides applied before the loop (see get_mode_params)"},
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return refineJob(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "start_run", Category: "tasks", Mutating: true,
		Description: "Start a new processing run over a capture folder — optionally with CHOSEN fine parameters (get_mode_params lists each mode's tunable knobs) and a quality goal the supervisor pursues. Use browse_files/inspect_captures first to find and confirm the folder.",
		Schema: objectSchema([]string{"path"}, map[string]any{
			"path":      strProp("capture folder (inside the data directory)"),
			"mode":      strProp("deepsky|nebula|milkyway|nightpano|planetary|comet|mosaic|sun|eclipse (default deepsky)"),
			"format":    strProp("image|video|both (default image)"),
			"supervise": boolProp("drive the local AI agent to auto-tune the finish"),
			"params":    map[string]any{"type": "object", "description": "fine knob overrides for the mode (see get_mode_params); clamped to safe ranges"},
			"goal":      strProp("free-text quality objective the supervisor optimizes for"),
			"tier":      strProp("supervisor re-entry ceiling A|B|C (default: mode default)"),
			"max_iters": intProp("max supervisor passes (0 = default)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return startRun(ctx, d, args) },
	})
}

// jobRow is the compact per-task view for list_jobs.
type jobRow struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Step      string `json:"step,omitempty"`
	Progress  int    `json:"progress,omitempty"`
	Error     string `json:"error,omitempty"`
	Object    string `json:"object,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func listJobs(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 20
	}
	jobs, err := d.Store.ListJobs(ctx, 100, 0)
	if err != nil {
		return "", err
	}
	rows := make([]jobRow, 0, in.Limit)
	for _, j := range jobs {
		if in.Status != "" && j.Status != in.Status {
			continue
		}
		rows = append(rows, jobRow{
			ID: j.ID, Kind: j.Kind, Status: j.Status, Step: j.CurrentStep,
			Progress: j.Progress, Error: j.Error, Object: resultObject(j.Result), CreatedAt: j.CreatedAt,
		})
		if len(rows) >= in.Limit {
			break
		}
	}
	return jsonResult(map[string]any{"count": len(rows), "jobs": rows})
}

// jobParamsView / jobResultView are minimal projections of the persisted params/result JSON, so the
// tools stay decoupled from job.RunRequest / pipeline.Result.
type jobParamsView struct {
	Mode      string `json:"mode"`
	Path      string `json:"path"`
	Supervise bool   `json:"supervise"`
}
type jobResultView struct {
	Object    string   `json:"object"`
	OutputDir string   `json:"output_dir"`
	RunID     string   `json:"run_id"`
	Warnings  []string `json:"warnings"`
	Final     *struct {
		Mode       string   `json:"mode"`
		Outputs    []string `json:"outputs"`
		Iterations []any    `json:"iterations"`
	} `json:"final"`
	Channels []any `json:"channels"`
}

func resultObject(raw json.RawMessage) string {
	var v struct {
		Object string `json:"object"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Object
}

func getJob(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	id, err := jobIDArg(args)
	if err != nil {
		return "", err
	}
	j, err := d.Store.GetJob(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get job %d: %w", id, err)
	}
	var params jobParamsView
	_ = json.Unmarshal(j.Params, &params)
	var res jobResultView
	_ = json.Unmarshal(j.Result, &res)
	out := map[string]any{
		"id": j.ID, "kind": j.Kind, "status": j.Status, "step": j.CurrentStep,
		"progress": j.Progress, "error": j.Error, "params": params,
		"created_at": j.CreatedAt, "finished_at": j.FinishedAtMs,
	}
	if len(j.Result) > 0 && string(j.Result) != "null" {
		summary := map[string]any{"object": res.Object, "output_dir": res.OutputDir, "run_id": res.RunID, "channels": len(res.Channels), "warnings": res.Warnings}
		if res.Final != nil {
			summary["final_mode"] = res.Final.Mode
			summary["outputs"] = res.Final.Outputs
			summary["iterations"] = len(res.Final.Iterations)
		}
		out["result"] = summary
	}
	return jsonResult(out)
}

func getJobIterations(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	id, err := jobIDArg(args)
	if err != nil {
		return "", err
	}
	iters, err := d.Store.ListFinishIterations(ctx, id)
	if err != nil {
		return "", fmt.Errorf("iterations of job %d: %w", id, err)
	}
	rows := make([]map[string]any, 0, len(iters))
	for _, it := range iters {
		rows = append(rows, map[string]any{
			"iteration": it.Iteration, "tier": it.Tier, "det_score": it.DetScore,
			"model_score": it.ModelScore, "combined_score": it.CombinedScore,
			"chosen": it.Chosen, "reasoning": it.Reasoning, "defects": json.RawMessage(it.Defects),
		})
	}
	return jsonResult(map[string]any{"count": len(rows), "iterations": rows})
}

func cancelJob(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	id, err := jobIDArg(args)
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"cancelled": d.Mgr.Cancel(id)})
}

func restartJob(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	id, err := jobIDArg(args)
	if err != nil {
		return "", err
	}
	newID, err := d.Mgr.Restart(ctx, id)
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"new_job_id": newID})
}

func refineJob(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		ID       int64           `json:"id"`
		Tier     string          `json:"tier"`
		MaxIters int             `json:"max_iters"`
		Params   json.RawMessage `json:"params"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if in.Tier == "" {
		in.Tier = "B"
	}
	newID, err := d.Mgr.Refine(ctx, in.ID, job.RefineRequest{Tier: in.Tier, MaxIters: in.MaxIters, Params: in.Params})
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"new_job_id": newID, "tier": in.Tier})
}

func startRun(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Path      string          `json:"path"`
		Mode      string          `json:"mode"`
		Format    string          `json:"format"`
		Supervise bool            `json:"supervise"`
		Params    json.RawMessage `json:"params"`
		Goal      string          `json:"goal"`
		Tier      string          `json:"tier"`
		MaxIters  int             `json:"max_iters"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	path, err := confinePath(in.Path, d.Cfg.DataDir)
	if err != nil {
		return "", err
	}
	req := job.RunRequest{
		Path: path, Mode: in.Mode, Format: in.Format, Supervise: in.Supervise,
		Params: in.Params, Goal: in.Goal, Tier: in.Tier, MaxIters: in.MaxIters,
	}
	if req.Mode == "" {
		req.Mode = "deepsky"
	}
	if req.Format == "" {
		req.Format = "image"
	}
	if req.Goal != "" { // a goal implies the supervisor pursues it
		req.Supervise = true
	}
	newID, err := d.Mgr.Enqueue(ctx, req)
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"job_id": newID, "mode": req.Mode, "path": path})
}

// jobIDArg extracts the required "id" from a tool call.
func jobIDArg(args json.RawMessage) (int64, error) {
	var in struct {
		ID int64 `json:"id"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return 0, err
	}
	if in.ID == 0 {
		return 0, fmt.Errorf("id is required")
	}
	return in.ID, nil
}
