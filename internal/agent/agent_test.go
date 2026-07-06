package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/llm"
)

func TestParseStep(t *testing.T) {
	tests := []struct {
		name     string
		reply    string
		wantOK   bool
		wantTool string
		wantFin  string
	}{
		{"plain tool", `{"thought":"x","tool":"list_jobs","args":{"status":"failed"}}`, true, "list_jobs", ""},
		{"fenced final", "```json\n{\"final\":\"done\"}\n```", true, "", "done"},
		{"prose-wrapped", `Sure: {"tool":"get_job","args":{"id":3}} ok`, true, "get_job", ""},
		{"garbage", "not json at all", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := parseStep(tt.reply)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantTool, s.Tool)
			assert.Equal(t, tt.wantFin, s.Final)
		})
	}
}

func TestParseStep_Ask(t *testing.T) {
	s, ok := parseStep(`{"thought":"choose","ask":{"question":"which?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}}`)
	require.True(t, ok)
	require.NotNil(t, s.Ask)
	assert.Equal(t, "which?", s.Ask.Question)
	require.Len(t, s.Ask.Options, 2)
	assert.Equal(t, "A", s.Ask.Options[0].Label)
}

func TestRegistry_MenuAndDispatch(t *testing.T) {
	r := NewRegistry()
	r.Add(Tool{Name: "read_it", Category: "misc", Description: "reads",
		Schema: objectSchema([]string{"id"}, map[string]any{"id": intProp("the id"), "note": strProp("a note")})})
	r.Add(Tool{Name: "do_it", Category: "misc", Mutating: true, Description: "acts"})

	menu := r.Menu()
	assert.Contains(t, menu, "read_it(")
	assert.Contains(t, menu, "id:integer")   // required arg has no ?
	assert.Contains(t, menu, "note?:string") // optional arg has ?
	assert.Contains(t, menu, "[mutating")    // the mutating marker on do_it

	_, ok := r.Get("read_it")
	assert.True(t, ok)
	_, ok = r.Get("missing")
	assert.False(t, ok)
}

// scriptedModel returns canned replies in order (then a default final), for the loop tests.
type scriptedModel struct {
	replies []string
	i       int
}

func (m *scriptedModel) Complete(_ context.Context, _ []llm.Message, _ llm.CompleteOptions) (string, error) {
	if m.i >= len(m.replies) {
		return `{"final":"(default)"}`, nil
	}
	r := m.replies[m.i]
	m.i++
	return r, nil
}

func runnerWith(replies []string, tools ...Tool) *Runner {
	reg := NewRegistry()
	for _, tl := range tools {
		reg.Add(tl)
	}
	model := &scriptedModel{replies: replies}
	return NewRunner(reg, func(string) Completer { return model })
}

func alwaysConfirm(approve bool, choice string) ConfirmFn {
	return func(context.Context, string, Event) (bool, string, bool) { return approve, choice, true }
}

func collect() (func(Event), *[]Event) {
	var events []Event
	return func(e Event) { events = append(events, e) }, &events
}

func kinds(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestRunTurn_ReadToolThenFinal(t *testing.T) {
	called := false
	read := Tool{Name: "echo", Category: "misc", Description: "echo",
		Handler: func(_ context.Context, args json.RawMessage) (string, error) {
			called = true
			return "echoed:" + string(args), nil
		}}
	rn := runnerWith([]string{
		`{"thought":"look","tool":"echo","args":{"x":"hi"}}`,
		`{"final":"the answer"}`,
	}, read)
	report, events := collect()

	final, err := rn.RunTurn(context.Background(), "m", nil, report, alwaysConfirm(true, ""))
	require.NoError(t, err)
	assert.Equal(t, "the answer", final)
	assert.True(t, called, "read tool should auto-run")
	assert.Contains(t, kinds(*events), "tool_call")
	assert.Contains(t, kinds(*events), "tool_result")
	assert.Contains(t, kinds(*events), "final")
}

func TestRunTurn_MutatingToolGated_Approved(t *testing.T) {
	ran := false
	act := Tool{Name: "act", Category: "misc", Mutating: true, Description: "act",
		Handler: func(context.Context, json.RawMessage) (string, error) { ran = true; return "done", nil }}
	rn := runnerWith([]string{`{"tool":"act","args":{}}`, `{"final":"ok"}`}, act)
	report, events := collect()

	confirmed := false
	confirm := func(context.Context, string, Event) (bool, string, bool) { confirmed = true; return true, "", true }
	final, err := rn.RunTurn(context.Background(), "m", nil, report, confirm)
	require.NoError(t, err)
	assert.Equal(t, "ok", final)
	assert.True(t, confirmed, "a mutating tool must request confirmation")
	assert.True(t, ran, "an approved mutating tool must run")
	assert.Contains(t, kinds(*events), "confirm")
}

func TestRunTurn_MutatingToolGated_Declined(t *testing.T) {
	ran := false
	act := Tool{Name: "act", Category: "misc", Mutating: true, Description: "act",
		Handler: func(context.Context, json.RawMessage) (string, error) { ran = true; return "done", nil }}
	rn := runnerWith([]string{`{"tool":"act","args":{}}`, `{"final":"ok"}`}, act)
	report, _ := collect()

	final, err := rn.RunTurn(context.Background(), "m", nil, report, alwaysConfirm(false, ""))
	require.NoError(t, err)
	assert.Equal(t, "ok", final)
	assert.False(t, ran, "a declined mutating tool must NOT run")
}

func TestRunTurn_SoftFailThenRecover(t *testing.T) {
	rn := runnerWith([]string{`not json`, `{"final":"recovered"}`})
	report, _ := collect()
	final, err := rn.RunTurn(context.Background(), "m", nil, report, alwaysConfirm(true, ""))
	require.NoError(t, err)
	assert.Equal(t, "recovered", final)
}

func TestRunTurn_UnknownToolBecomesObservation(t *testing.T) {
	rn := runnerWith([]string{`{"tool":"nope","args":{}}`, `{"final":"handled"}`})
	report, events := collect()
	final, err := rn.RunTurn(context.Background(), "m", nil, report, alwaysConfirm(true, ""))
	require.NoError(t, err)
	assert.Equal(t, "handled", final)
	// an unknown tool yields an error tool_result, not a crash
	var sawErr bool
	for _, e := range *events {
		if e.Kind == "tool_result" && e.IsError {
			sawErr = true
		}
	}
	assert.True(t, sawErr)
}

func TestSystemPrompt_ContainsToolsAndContract(t *testing.T) {
	r := NewRegistry()
	r.Add(Tool{Name: "list_jobs", Category: "tasks", Description: "list tasks"})
	p := SystemPrompt(r)
	assert.True(t, strings.Contains(p, "list_jobs"))
	assert.True(t, strings.Contains(p, `"final"`)) // the JSON contract is documented
}
