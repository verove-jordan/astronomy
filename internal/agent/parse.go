package agent

import (
	"encoding/json"
	"strings"
)

// askUser is the agent asking the user to pick among options before it proceeds (e.g. "which fix?").
type askUser struct {
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

// step is one model turn in the ReAct loop: a thought plus exactly one of tool / ask / final. The
// model is instructed to emit only these fields as a single JSON object.
type step struct {
	Thought string          `json:"thought"`
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Ask     *askUser        `json:"ask"`
	Final   string          `json:"final"`
}

// parseStep extracts the JSON step from a model reply (tolerant of prose or ``` fences). It returns
// ok=false on unparseable output so the loop can re-prompt once, then finalize — it never panics.
func parseStep(reply string) (step, bool) {
	var s step
	if err := json.Unmarshal([]byte(extractJSON(reply)), &s); err != nil {
		return step{}, false
	}
	return s, true
}

// extractJSON pulls the first {...} object out of a reply that may wrap it in prose or fences. Mirrors
// the finish supervisor's helper (internal/pipeline) — duplicated because it is unexported there.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
