// Command siril-mcp is an MCP server (stdio) that brokers a host-installed Siril for Claude,
// exposing the AstroStack engine — inspect, the calibration library, the full auto pipeline, and
// a Siril script escape hatch — as MCP tools. It shares the internal/ engine with `astrostack`.
//
// Mirrors the project's GIMP MCP conventions: a `health` start-here probe, a read/write tool split
// (each mutating tool is separate so Claude prompts per action), rich tool descriptions, and an
// escape hatch. Config comes from env (SIRIL_BIN, DATABASE_URL, ASTRO_*).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mcpserver"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/report"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/store"
)

const version = "0.1.0"

type app struct {
	cfg    *config.Config
	runner *siril.Runner
	store  *store.Store // may be nil if the DB is unreachable
}

func main() {
	cfg := config.Load()
	a := &app{cfg: cfg, runner: siril.New(cfg.SirilBin)}
	if st, err := store.New(context.Background(), cfg.DatabaseURL); err != nil {
		fmt.Fprintf(os.Stderr, "siril-mcp: calibration library disabled (%v)\n", err)
	} else {
		a.store = st
		defer st.Close()
	}

	s := mcpserver.New("siril", version)
	a.register(s)
	if err := s.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "siril-mcp:", err)
		os.Exit(1)
	}
}

func (a *app) register(s *mcpserver.Server) {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object"}
		if props != nil {
			schema["properties"] = props
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

	// --- read-only ---
	s.AddTool(mcpserver.Tool{
		Name: "health",
		Description: "Readiness probe and the place to START. Reports whether host Siril (siril-cli) " +
			"is available and its version, whether the calibration-library database is reachable, " +
			"and the configured data/work/output directories.",
		Handler: a.health,
	})
	s.AddTool(mcpserver.Tool{
		Name: "inspect_directory",
		Description: "Scan a capture directory and return the classified inventory: light frames per " +
			"filter (L/R/G/B/Ha…), darks, flats, bias, dark-flats and videos, grouped into sets with " +
			"exposure/gain/offset/temperature, plus warnings about missing calibration. Read-only.",
		InputSchema: obj(map[string]any{"path": str("absolute path to the capture directory")}, "path"),
		Handler:     a.inspectDirectory,
	})
	s.AddTool(mcpserver.Tool{
		Name:        "list_masters",
		Description: "List the reusable master calibration frames in the library (type, filter, exposure, gain, offset, temperature, frame count, path). Requires the database.",
		Handler:     a.listMasters,
	})

	// --- write / heavy (each prompts separately) ---
	s.AddTool(mcpserver.Tool{
		Name: "run_pipeline",
		Description: "Run the FULL automatic deep-sky pipeline on a directory: inspect → build/reuse " +
			"master calibration → calibrate → grade & reject bad sub-frames (trails, soft, clouds, " +
			"elongated) → register → stack per channel → combine (LRGB/HaRGB/SHO) → post-process. " +
			"Returns a full run report. Long-running; drives host Siril.",
		InputSchema: obj(map[string]any{
			"path": str("absolute path to the capture directory"),
			"out":  str("output directory (optional; defaults to ASTRO_OUTPUT_DIR)"),
			"work": str("scratch directory (optional; defaults to ASTRO_WORK_DIR)"),
		}, "path"),
		Handler: a.runPipeline,
	})
	s.AddTool(mcpserver.Tool{
		Name: "process_video",
		Description: "Process a lunar/planetary VIDEO (SER/AVI/MP4/MOV) with lucky imaging: extract " +
			"frames, rank by sharpness, keep the best, stack and sharpen. Returns the output paths.",
		InputSchema: obj(map[string]any{
			"path": str("absolute path to the video file"),
			"best": map[string]any{"type": "integer", "description": "keep this percent of the sharpest frames (default 50)"},
		}, "path"),
		Handler: a.processVideo,
	})
	s.AddTool(mcpserver.Tool{
		Name: "eval_ssf",
		Description: "Escape hatch: run an arbitrary Siril script (.ssf commands, newline-separated) via " +
			"siril-cli and return its log. Use for one-off Siril operations the dedicated tools don't cover.",
		InputSchema: obj(map[string]any{
			"script": str("Siril .ssf script body (e.g. 'requires 1.2.0\\nsetext fits\\nload x\\nautostretch\\nsavepng y')"),
			"dir":    str("working directory for the script (optional; defaults to ASTRO_WORK_DIR)"),
		}, "script"),
		Handler: a.evalSSF,
	})
}

func (a *app) health(ctx context.Context, _ json.RawMessage) (string, error) {
	sirilStatus := "OK"
	if err := a.runner.Available(ctx); err != nil {
		sirilStatus = "UNAVAILABLE — " + err.Error()
	}
	db := "disabled (no database)"
	if a.store != nil {
		db = "connected"
	}
	return fmt.Sprintf("# siril-mcp health\nSiril (%s): %s\nCalibration library: %s\nData dir:   %s\nWork dir:   %s\nOutput dir: %s\nLibrary:    %s",
		a.cfg.SirilBin, sirilStatus, db, a.cfg.DataDir, a.cfg.WorkDir, a.cfg.OutputDir, a.cfg.LibraryDir), nil
}

func (a *app) inspectDirectory(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	inv, err := inspect.Scan(ctx, p.Path)
	if err != nil {
		return "", err
	}
	return report.InventoryText(inv), nil
}

func (a *app) listMasters(ctx context.Context, _ json.RawMessage) (string, error) {
	if a.store == nil {
		return "", fmt.Errorf("database not configured (set DATABASE_URL and run migrations)")
	}
	masters, err := a.store.ListMasters(ctx)
	if err != nil {
		return "", err
	}
	if len(masters) == 0 {
		return "calibration library is empty", nil
	}
	out := fmt.Sprintf("%d master(s) in the library:\n", len(masters))
	for _, m := range masters {
		out += fmt.Sprintf("  %-9s filter=%-3s exp=%-7dms gain=%d offset=%d temp=%d°C frames=%d  %s\n",
			m.Type, dash(m.Filter), m.ExposureMs, m.Gain, m.Offset, m.TempMilliC/1000, m.FrameCount, m.Path)
	}
	return out, nil
}

func (a *app) runPipeline(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
		Out  string `json:"out"`
		Work string `json:"work"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	var library calib.MasterStore
	if a.store != nil {
		library = a.store
	}
	res, err := pipeline.Process(ctx, pipeline.Options{
		InputDir:   p.Path,
		OutputDir:  pick(p.Out, a.cfg.OutputDir),
		WorkDir:    pick(p.Work, a.cfg.WorkDir),
		Runner:     a.runner,
		Graxpert:   graxpert.New(a.cfg.GraxpertBin),
		Starnet:    starnet.New(a.cfg.StarnetBin),
		Library:    library,
		LibraryDir: a.cfg.LibraryDir,
		OnProgress: func(pr pipeline.Progress) {
			if pr.Line == "" {
				fmt.Fprintf(os.Stderr, "[siril-mcp] %s\n", pr.Step)
			}
		},
	})
	if err != nil {
		return "", err
	}
	return report.RunText(res), nil
}

func (a *app) processVideo(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
		Best int    `json:"best"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	opts := planetary.DefaultOptions()
	if p.Best > 0 {
		opts.BestPercent = p.Best
	}
	res, err := planetary.Process(ctx, a.runner, a.cfg.FfmpegBin, p.Path, a.cfg.WorkDir, a.cfg.OutputDir, opts,
		func(pr siril.Progress) {
			if pr.Line != "" {
				fmt.Fprintf(os.Stderr, "[siril-mcp] %s\n", pr.Line)
			}
		})
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("Planetary stack of %s\nFrames: %d total, %d stacked\nOutputs:\n", res.Source, res.FrameCount, res.StackedFrames)
	for _, o := range res.Outputs {
		out += "  " + o + "\n"
	}
	return out, nil
}

func (a *app) evalSSF(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Script string `json:"script"`
		Dir    string `json:"dir"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Script == "" {
		return "", fmt.Errorf("script is required")
	}
	res, err := a.runner.Run(ctx, pick(p.Dir, a.cfg.WorkDir), p.Script, nil)
	if res != nil {
		return res.Log, err
	}
	return "", err
}

func pick(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
