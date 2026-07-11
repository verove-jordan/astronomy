package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/llm"
)

const (
	maxSteps       = 12   // hard cap on tool/ask iterations per turn
	maxObservation = 6000 // truncate a tool observation to bound context growth
	stepMaxTokens  = 2000 // room for a long final critique or a tool call
)

// ConfirmFn asks the user to approve a mutating action or pick among options, blocking until they
// answer (or the turn is cancelled). ok=false means the turn was cancelled before an answer arrived;
// choice is the selected option id (for an ask), approve whether a mutating action was allowed.
type ConfirmFn func(ctx context.Context, callID string, e Event) (approve bool, choice string, ok bool)

// Completer is the minimal model client the loop needs (satisfied by *llm.Runner). An interface keeps
// the loop testable with a scripted fake.
type Completer interface {
	Complete(ctx context.Context, msgs []llm.Message, opts llm.CompleteOptions) (string, error)
}

// Runner drives an agent turn against the tool registry and a per-turn model client.
type Runner struct {
	reg      *Registry
	newModel func(model string) Completer
}

// NewRunner builds an agent runner. newModel returns a model client for a given model id (so each turn
// uses the caller's configured base URL / image format / timeout).
func NewRunner(reg *Registry, newModel func(model string) Completer) *Runner {
	return &Runner{reg: reg, newModel: newModel}
}

// Registry exposes the wired tools (for the system prompt).
func (rn *Runner) Registry() *Registry { return rn.reg }

// RunTurn executes one agent turn: repeatedly ask the model for a JSON step, run read tools
// automatically, gate mutating tools behind confirm, and stream every step via report — until the
// model gives a final answer or the step cap is reached. seed is the full message history (system
// prompt + prior turns + image grounding) built by the caller.
func (rn *Runner) RunTurn(ctx context.Context, model string, seed []llm.Message,
	report func(Event), confirm ConfirmFn) (string, error) {
	msgs := append([]llm.Message(nil), seed...)
	client := rn.newModel(model)
	callSeq := 0

	for step := 1; step <= maxSteps; step++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		reply, err := client.Complete(ctx, msgs, llm.CompleteOptions{Temperature: 0.2, MaxTokens: stepMaxTokens, JSON: true})
		if err != nil {
			report(Event{Kind: "error", Step: step, Text: err.Error()})
			return "", err
		}
		st, ok := parseStep(reply)
		if !ok {
			msgs = append(msgs, assistantMsg(reply),
				userMsg("Your last message was not a single valid JSON step. Reply with exactly ONE JSON object as instructed."))
			continue
		}
		if th := strings.TrimSpace(st.Thought); th != "" {
			report(Event{Kind: "thinking", Step: step, Text: th})
		}
		if final := strings.TrimSpace(st.Final); final != "" {
			report(Event{Kind: "final", Step: step, Text: final})
			return final, nil
		}

		observation, obsImages, cancelled := rn.act(ctx, step, st, report, confirm, &callSeq)
		if cancelled {
			return "", ctx.Err()
		}
		obsMsg := userMsg("Observation:\n" + observation)
		obsMsg.Images = obsImages // vision-in-the-loop: image-returning tools attach what the model must SEE
		msgs = append(msgs, assistantMsg(reply), obsMsg)
	}
	return rn.finalize(ctx, client, msgs, report)
}

// act runs the step's tool or handles its ask, returning the observation to feed back. cancelled=true
// means the turn's context was cancelled while awaiting a confirmation.
func (rn *Runner) act(ctx context.Context, step int, st step, report func(Event), confirm ConfirmFn, callSeq *int) (string, []llm.InlineImage, bool) {
	switch {
	case st.Ask != nil && strings.TrimSpace(st.Ask.Question) != "":
		obs, cancelled := rn.ask(ctx, step, st.Ask, report, confirm, callSeq)
		return obs, nil, cancelled
	case strings.TrimSpace(st.Tool) != "":
		return rn.runTool(ctx, step, st, report, confirm, callSeq)
	default:
		return "Empty step: call a tool, ask the user, or give a final answer.", nil, false
	}
}

// ask presents a multiple-choice question to the user and blocks for their pick.
func (rn *Runner) ask(ctx context.Context, step int, a *askUser, report func(Event), confirm ConfirmFn, callSeq *int) (string, bool) {
	*callSeq++
	callID := fmt.Sprintf("c%d", *callSeq)
	report(Event{Kind: "ask", Step: step, CallID: callID, Question: a.Question, Options: a.Options})
	_, choice, ok := confirm(ctx, callID, Event{Kind: "ask", Options: a.Options})
	if !ok {
		return "", true
	}
	if choice == "" {
		return "The user dismissed the question without choosing. Ask again more simply or proceed with a safe default.", false
	}
	return "The user chose: " + optionLabel(a.Options, choice), false
}

// runTool executes one tool, gating a mutating tool behind a user confirmation first. Tool errors
// become observations (the model can recover); only a cancelled context returns cancelled=true.
func (rn *Runner) runTool(ctx context.Context, step int, st step, report func(Event), confirm ConfirmFn, callSeq *int) (string, []llm.InlineImage, bool) {
	tool, ok := rn.reg.Get(st.Tool)
	if !ok {
		report(Event{Kind: "tool_result", Step: step, Tool: st.Tool, IsError: true, Output: "unknown tool"})
		return fmt.Sprintf("Error: unknown tool %q. Pick a tool from the menu.", st.Tool), nil, false
	}
	report(Event{Kind: "tool_call", Step: step, Tool: tool.Name, Args: string(st.Args), Mutating: tool.Mutating})
	if tool.Mutating {
		*callSeq++
		callID := fmt.Sprintf("c%d", *callSeq)
		report(Event{Kind: "confirm", Step: step, CallID: callID, Tool: tool.Name, Args: string(st.Args),
			Mutating: true, Question: tool.Description})
		approve, _, ok := confirm(ctx, callID, Event{Kind: "confirm", Tool: tool.Name})
		if !ok {
			return "", nil, true
		}
		if !approve {
			report(Event{Kind: "tool_result", Step: step, Tool: tool.Name, Output: "user declined the action"})
			return "The user declined this action. Do not retry it; adapt or ask what they'd prefer instead.", nil, false
		}
	}
	var out string
	var imgs []llm.InlineImage
	var err error
	if tool.ImageHandler != nil {
		out, imgs, err = tool.ImageHandler(ctx, st.Args)
	} else {
		out, err = tool.Handler(ctx, st.Args)
	}
	if err != nil {
		report(Event{Kind: "tool_result", Step: step, Tool: tool.Name, IsError: true, Output: err.Error()})
		return fmt.Sprintf("Error from %s: %s", tool.Name, err.Error()), nil, false
	}
	out = truncate(out, maxObservation)
	report(Event{Kind: "tool_result", Step: step, Tool: tool.Name, Output: out})
	return out, imgs, false
}

// finalize asks the model for its best final answer once the step cap is hit, so a long investigation
// still ends with an answer rather than silence.
func (rn *Runner) finalize(ctx context.Context, client Completer, msgs []llm.Message, report func(Event)) (string, error) {
	msgs = append(msgs, userMsg(`You have reached the step limit. Give your best final answer now as {"final":"..."} in the user's language.`))
	reply, err := client.Complete(ctx, msgs, llm.CompleteOptions{Temperature: 0.2, MaxTokens: stepMaxTokens, JSON: true})
	if err != nil {
		report(Event{Kind: "error", Text: err.Error()})
		return "", err
	}
	final := reply
	if st, ok := parseStep(reply); ok && strings.TrimSpace(st.Final) != "" {
		final = st.Final
	}
	report(Event{Kind: "final", Text: final})
	return final, nil
}

func assistantMsg(text string) llm.Message { return llm.Message{Role: "assistant", Text: text} }
func userMsg(text string) llm.Message      { return llm.Message{Role: "user", Text: text} }

// optionLabel returns an option's label for the observation, falling back to the raw id.
func optionLabel(options []Option, id string) string {
	for _, o := range options {
		if o.ID == id {
			if o.Label != "" {
				return o.Label
			}
			return o.ID
		}
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}
