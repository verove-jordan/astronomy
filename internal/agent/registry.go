// Package agent runs a local model as a tool-using assistant over the whole AstroStack app. It is a
// bounded ReAct loop: the model emits one structured-JSON step at a time (a thought plus exactly one
// of — call a tool, ask the user to choose, or give a final answer); the engine runs read-only tools
// automatically and gates every state-changing tool behind an explicit user confirmation, streaming
// each step to the UI. Tool-calling is EMULATED via response_format:json_object because the local
// model server (mlx-vlm) has no native function-calling. The tool handlers are thin wrappers over the
// engine's existing services (jobs, sky, weather, light pollution, files, config…).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Tool is one capability the agent can invoke. Handler executes it and returns a compact text/JSON
// observation fed back to the model. A Mutating tool changes app state (start/cancel/restart a run,
// transfer files, build the atlas…) and is only executed after the user approves a confirmation card.
type Tool struct {
	Name        string
	Description string
	Category    string         // grouping for the tool menu + UI (e.g. "tasks", "sky", "setup")
	Mutating    bool           // true → gated behind a user confirmation before Handler runs
	Schema      map[string]any // JSON-Schema object describing the args (rendered into the prompt)
	Handler     func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds the agent's tools, indexed by name.
type Registry struct {
	tools []Tool
	index map[string]Tool
}

// NewRegistry returns an empty registry; callers Add tools (see tools_*.go, wired by NewToolset).
func NewRegistry() *Registry {
	return &Registry{index: map[string]Tool{}}
}

// Add registers a tool. A duplicate name is a wiring bug, so it panics at startup rather than silently
// shadowing.
func (r *Registry) Add(t Tool) {
	if _, dup := r.index[t.Name]; dup {
		panic(fmt.Sprintf("agent: duplicate tool %q", t.Name))
	}
	r.tools = append(r.tools, t)
	r.index[t.Name] = t
}

// Get returns the tool with the given name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.index[name]
	return t, ok
}

// List returns the registered tools in registration order.
func (r *Registry) List() []Tool { return r.tools }

// Menu renders the tool catalog for the system prompt: tools grouped by category, one line each with
// a compact arg signature and a [mutating] marker so the model knows which calls need confirmation.
func (r *Registry) Menu() string {
	byCat := map[string][]Tool{}
	var cats []string
	for _, t := range r.tools {
		if _, seen := byCat[t.Category]; !seen {
			cats = append(cats, t.Category)
		}
		byCat[t.Category] = append(byCat[t.Category], t)
	}
	sort.Strings(cats)

	var b strings.Builder
	for _, cat := range cats {
		fmt.Fprintf(&b, "\n[%s]\n", cat)
		for _, t := range byCat[cat] {
			marker := ""
			if t.Mutating {
				marker = " [mutating — needs user confirmation]"
			}
			fmt.Fprintf(&b, "- %s(%s)%s: %s\n", t.Name, argSignature(t.Schema), marker, t.Description)
		}
	}
	return b.String()
}

// argSignature renders a tool's args as a compact "name:type, name?:type" list (required args first,
// optional args suffixed with ?), from the JSON-Schema properties/required. Empty schema → "".
func argSignature(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return ""
	}
	required := map[string]bool{}
	if req, ok := schema["required"].([]string); ok {
		for _, name := range req {
			required[name] = true
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		typ := "any"
		if spec, ok := props[name].(map[string]any); ok {
			if s, ok := spec["type"].(string); ok {
				typ = s
			}
		}
		opt := "?"
		if required[name] {
			opt = ""
		}
		parts = append(parts, fmt.Sprintf("%s%s:%s", name, opt, typ))
	}
	return strings.Join(parts, ", ")
}
