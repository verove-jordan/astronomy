package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// registerFileTools exposes the capture file browser, a capture-folder inspector and the calibration
// library (all read-only, confined to the data directory).
func registerFileTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "browse_files", Category: "files",
		Description: "List sub-folders and files of a directory (defaults to the data root). Use it to discover capture folders before inspecting or processing them.",
		Schema:      objectSchema(nil, map[string]any{"path": strProp("directory to list (default = data root)")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return browseFiles(d, args) },
	})
	r.Add(Tool{
		Name: "inspect_captures", Category: "files",
		Description: "Scan a capture folder and summarise it: frame types, per-filter light counts, exposures, and detected channel order — what a run over it would stack.",
		Schema:      objectSchema([]string{"path"}, map[string]any{"path": strProp("capture folder to inspect")}),
		Handler:     func(ctx context.Context, args json.RawMessage) (string, error) { return inspectCaptures(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "list_masters", Category: "files",
		Description: "List the deep-sky calibration masters (dark/flat/bias) in the library, with their filter/exposure/gain/temperature.",
		Schema:      objectSchema(nil, map[string]any{}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			m, err := d.Store.ListMasters(ctx)
			if err != nil {
				return "", err
			}
			return jsonResult(map[string]any{"count": len(m), "masters": m})
		},
	})
	r.Add(Tool{
		Name: "list_phone_masters", Category: "files",
		Description: "List the phone/DSLR (iPhone DNG) calibration masters, keyed by ISO/exposure/dimensions.",
		Schema:      objectSchema(nil, map[string]any{}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			m, err := d.Store.ListPhoneMasters(ctx)
			if err != nil {
				return "", err
			}
			return jsonResult(map[string]any{"count": len(m), "masters": m})
		},
	})
}

func browseFiles(d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	path := in.Path
	if strings.TrimSpace(path) == "" {
		path = d.Cfg.DataDir
	}
	abs, err := confinePath(path, d.Cfg.DataDir)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("read dir: %w", err)
	}
	type entry struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	var dirs, files []entry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		it := entry{Name: e.Name(), Path: filepath.Join(abs, e.Name()), IsDir: e.IsDir()}
		if e.IsDir() {
			dirs = append(dirs, it)
		} else {
			files = append(files, it)
		}
	}
	return jsonResult(map[string]any{"path": abs, "entries": append(dirs, files...)})
}

func inspectCaptures(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	abs, err := confinePath(in.Path, d.Cfg.DataDir)
	if err != nil {
		return "", err
	}
	inv, err := inspect.ScanMany(ctx, []string{abs}, inspect.DefaultScanOptions())
	if err != nil {
		return "", fmt.Errorf("scan: %w", err)
	}
	sets := make([]map[string]any, 0, len(inv.Sets))
	for _, s := range inv.Sets {
		sets = append(sets, map[string]any{
			"type": s.Key.Type, "filter": s.Key.Filter, "count": s.Count,
			"exposure_ms": s.Key.ExposureMs, "gain": s.Key.Gain,
		})
	}
	out := map[string]any{"root": inv.Root, "frames": len(inv.Frames), "sets": sets, "warnings": inv.Warnings}
	if inv.ChannelDetection != nil {
		out["detected_order"] = inv.ChannelDetection.Order
	}
	return jsonResult(out)
}
