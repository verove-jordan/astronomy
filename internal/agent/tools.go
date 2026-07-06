package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/darksky"
	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/skyevents"
	"github.com/verove-jordan/astronomy/internal/skyplan"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// Deps bundles the live engine services the tool handlers wrap. It is the same set the API server
// already holds, so NewToolset(deps) wires the agent to the running app with no new business logic.
type Deps struct {
	Mgr            *job.Manager
	Store          *store.Store
	Planner        *skyplan.Planner
	Events         *skyevents.Engine
	LightPollution *lightpollution.Provider
	DarkSky        *darksky.Finder
	Weather        *weather.Provider
	Cfg            *config.Config
}

// NewToolset builds the full agent tool registry from the engine's services.
func NewToolset(d Deps) *Registry {
	r := NewRegistry()
	registerJobTools(r, d)
	registerSetupTools(r, d)
	registerSkyTools(r, d)
	registerConditionTools(r, d)
	registerFileTools(r, d)
	registerStorageTools(r, d)
	return r
}

// --- schema + arg helpers (mirror the siril-mcp obj/str/... style) ------------------------------

func objectSchema(required []string, props map[string]any) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func numProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// decodeArgs unmarshals tool args into v; empty/null args leave v at its zero value (all-optional tools).
func decodeArgs(args json.RawMessage, v any) error {
	s := strings.TrimSpace(string(args))
	if s == "" || s == "null" {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid args: %w", err)
	}
	return nil
}

// jsonResult marshals a tool's result to compact JSON for the model observation.
func jsonResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// confinePath resolves path to an absolute path and verifies it stays inside root (the data dir), so a
// model-supplied path can never escape the sandbox. Mirrors the API's resolveRoots/withinData guard.
func confinePath(path, root string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must be inside the data directory", path)
	}
	return abs, nil
}
