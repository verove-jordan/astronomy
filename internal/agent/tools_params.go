// Parameter + vision tools: the pieces that turn the chat agent into a closed processing loop — it
// reads a mode's tunable surface, SEES a run's result image, and retries the run with chosen
// parameters, inheriting the target's cross-run iteration memory (warm start).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
)

func registerParamTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "get_mode_params", Category: "tasks",
		Description: "Get a processing mode's tunable parameter surface: every knob, its safe range and its current default — the vocabulary for start_run/retry_run_tuned params.",
		Schema:      objectSchema([]string{"mode"}, map[string]any{"mode": strProp("deepsky|nebula|milkyway|nightpano|planetary|comet|mosaic|sun|eclipse")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return getModeParams(args) },
	})
	r.Add(Tool{
		Name: "retry_run_tuned", Category: "tasks", Mutating: true,
		Description: "Retry a finished run with CHOSEN parameter changes: clones the source run's setup, applies your params, and re-processes under the AI supervisor (which inherits the target's iteration memory). restack=true re-stacks from the raw frames (structural changes: frame selection, grading, deconvolution); false re-finishes only (colour/stretch/composite — much faster).",
		Schema: objectSchema([]string{"id"}, map[string]any{
			"id":        intProp("the source job id"),
			"params":    map[string]any{"type": "object", "description": "knob overrides (see get_mode_params); clamped to safe ranges"},
			"goal":      strProp("free-text quality objective for this attempt"),
			"restack":   boolProp("true = full re-process from the raw frames; false = re-finish only (default)"),
			"max_iters": intProp("max supervisor passes (0 = default)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return retryRunTuned(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "view_result_image", Category: "tasks",
		Description: "LOOK at a run's final image: attaches the rendered result (full frame + a 100% centre crop) to your context along with objective measurements — use it to judge quality and decide parameter changes before retry_run_tuned.",
		Schema:      objectSchema([]string{"id"}, map[string]any{"id": intProp("the job id whose final image to view")}),
		ImageHandler: func(ctx context.Context, args json.RawMessage) (string, []llm.InlineImage, error) {
			return viewResultImage(ctx, d, args)
		},
	})
}

func getModeParams(args json.RawMessage) (string, error) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	mo, err := mode.ParseMode(in.Mode)
	if err != nil {
		return "", err
	}
	preset := mode.For(mo)
	return jsonResult(map[string]any{
		"mode":     string(mo),
		"defaults": pipeline.ParamsFor(preset),
		"menu":     pipeline.KnobMenuFor(mo),
	})
}

func retryRunTuned(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		ID       int64           `json:"id"`
		Params   json.RawMessage `json:"params"`
		Goal     string          `json:"goal"`
		Restack  bool            `json:"restack"`
		MaxIters int             `json:"max_iters"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if in.Restack {
		// Full re-process: replay the source run's request with the tuned params. The supervisor
		// warm-starts from the target's best prior iteration, so this attempt continues the
		// improvement trajectory instead of starting cold.
		newID, err := d.Mgr.RetryTuned(ctx, in.ID, in.Params, in.Goal, in.MaxIters)
		if err != nil {
			return "", err
		}
		return jsonResult(map[string]any{"new_job_id": newID, "restack": true})
	}
	newID, err := d.Mgr.Refine(ctx, in.ID, job.RefineRequest{Tier: "B", MaxIters: in.MaxIters, Params: in.Params})
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"new_job_id": newID, "restack": false, "tier": "B"})
}

// viewResultImage resolves a finished job's final PNG, measures it objectively, and returns the
// grounding report plus the image (downscaled full frame + native centre crop) for the model to SEE.
func viewResultImage(ctx context.Context, d Deps, args json.RawMessage) (string, []llm.InlineImage, error) {
	id, err := jobIDArg(args)
	if err != nil {
		return "", nil, err
	}
	j, err := d.Store.GetJob(ctx, id)
	if err != nil {
		return "", nil, fmt.Errorf("get job %d: %w", id, err)
	}
	var res jobResultView
	_ = json.Unmarshal(j.Result, &res)
	png := ""
	if res.Final != nil {
		for _, o := range res.Final.Outputs {
			if strings.HasSuffix(o, ".png") {
				png = o
				break
			}
		}
	}
	if png == "" {
		return "", nil, fmt.Errorf("job %d has no final PNG to view", id)
	}
	confined, err := confinePath(png, d.Cfg.OutputDir)
	if err != nil {
		return "", nil, fmt.Errorf("job %d result outside the output dir: %w", id, err)
	}
	report, imgs, err := pipeline.ResultImagePayload(confined)
	if err != nil {
		return "", nil, err
	}
	name := filepath.Base(confined)
	return fmt.Sprintf("Viewing %s (job %d).\n%s\nImage 1 = full frame, image 2 = 100%% centre crop.", name, id, report), imgs, nil
}
