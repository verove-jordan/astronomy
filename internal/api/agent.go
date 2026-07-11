package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/agent"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/pipeline"
)

// agentTurnTimeout bounds one agent turn (incl. time spent waiting on user confirmations) so a turn
// left un-answered can never leak its goroutine or model connection.
const agentTurnTimeout = 30 * time.Minute

// buildAgent wires the tool-using AstroAgent from the server's live services and a per-turn model
// client factory. Called once from New.
func (s *Server) buildAgent() {
	reg := agent.NewToolset(agent.Deps{
		Mgr: s.mgr, Store: s.store, Planner: s.planner, Events: s.events,
		LightPollution: s.lightpollution, DarkSky: s.darksky, Weather: s.weather, Cfg: s.cfg,
	})
	s.agent = agent.NewRunner(reg, func(model string) agent.Completer {
		return llm.New(s.cfg.LLMBaseURL, model, s.cfg.LLMImageFormat).WithTimeout(s.cfg.LLMTimeout)
	})
	// s.agentTurns is the shared turn hub, injected by New (also used by the job manager).
}

// agentStatus reports whether the configured local vision model server is reachable and which model
// ids it advertises, so the UI can gate the AstroAgent page and populate its model picker. It never
// fails the request — a down/unconfigured server is reported as running=false. GET /api/agent/status
func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	runner := llm.New(s.cfg.LLMBaseURL, s.cfg.LLMModel, s.cfg.LLMImageFormat)
	models, err := runner.Models(r.Context())
	if models == nil {
		models = []string{}
	}
	resp := map[string]any{
		"running": err == nil,
		"model":   s.cfg.LLMModel, // configured default; "" means the user must pick one
		"models":  models,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// agentChatMessage is one turn in a chat request; images are browser data URLs (FileReader output).
type agentChatMessage struct {
	Role   string   `json:"role"`
	Text   string   `json:"text"`
	Images []string `json:"images"`
}

type agentChatRequest struct {
	Messages    []agentChatMessage `json:"messages"`
	Model       string             `json:"model"` // optional override; defaults to cfg.LLMModel
	Temperature float64            `json:"temperature"`
	MaxTokens   int                `json:"max_tokens"`
}

// agentChat starts a tool-using AstroAgent turn and returns its turn id (the caller then opens the SSE
// stream to watch the agent think, call tools, ask for confirmations, and answer). It seeds the turn
// with the agent system prompt (the live tool menu) + the conversation history + a measured "ground
// truth" report for each uploaded image, and returns the latest image's measurements so the UI can show
// its stats panel immediately. The model id is injected server-side (request override or cfg.LLMModel),
// since mlx-vlm 500s on a blank id. POST /api/agent/chat → 202 {turn_id, measurements}
func (s *Server) agentChat(w http.ResponseWriter, r *http.Request) {
	var body agentChatRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if len(body.Messages) == 0 {
		badRequest(w, "messages required")
		return
	}
	sys := agent.SystemPrompt(s.agent.Registry())
	if extra := strings.TrimSpace(s.cfg.LLMAssistPromptExtra); extra != "" {
		sys += "\n\n" + extra
	}
	msgs, measurements, err := buildAssistMessages(sys, body.Messages)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	model := body.Model
	if model == "" {
		model = s.cfg.LLMModel
	}
	if model == "" {
		badRequest(w, "no model configured (set ASTRO_LLM_MODEL or pass a model)")
		return
	}
	turnID := s.agentTurns.Start()
	go s.runAgentTurn(turnID, model, msgs)
	writeJSON(w, http.StatusAccepted, map[string]any{"turn_id": turnID, "measurements": measurements})
}

// runAgentTurn drives one agent turn to completion in the background, streaming every step to the turn's
// SSE subscribers. It uses its own bounded context (independent of the POST request) so the turn — which
// may pause for user confirmation — survives the request returning, and can never leak.
func (s *Server) runAgentTurn(turnID, model string, seed []llm.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), agentTurnTimeout)
	defer cancel()
	report := func(e agent.Event) { s.agentTurns.Publish(turnID, e) }
	confirm := func(ctx context.Context, callID string, _ agent.Event) (bool, string, bool) {
		return s.agentTurns.Await(ctx, turnID, callID)
	}
	if _, err := s.agent.RunTurn(ctx, model, seed, report, confirm); err != nil {
		s.agentTurns.Publish(turnID, agent.Event{Kind: "error", Text: err.Error()})
	}
	s.agentTurns.Finish(turnID)
}

// agentTurnEvents streams an agent turn's steps (thinking / tool calls / results / confirmation
// requests / final answer) to the browser over SSE, backlog-first so a late reader sees the whole run.
// GET /api/agent/turns/{id}/events
func (s *Server) agentTurnEvents(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		serverError(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	backlog, live, cancel, ok := s.agentTurns.Subscribe(turnID)
	if !ok {
		sendAgentEvent(w, flusher, agent.Event{Kind: "done"}) // unknown/expired turn → let the client stop
		return
	}
	defer cancel()
	for _, e := range backlog {
		sendAgentEvent(w, flusher, e)
		if e.Kind == "done" {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-live:
			if !open {
				return
			}
			sendAgentEvent(w, flusher, e)
			if e.Kind == "done" {
				return
			}
		}
	}
}

// agentTurnConfirm delivers the user's answer to a pending confirmation/choice, unblocking the turn's
// loop. POST /api/agent/turns/{id}/confirm  {call_id, approve, choice?}
func (s *Server) agentTurnConfirm(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")
	var body struct {
		CallID  string `json:"call_id"`
		Approve bool   `json:"approve"`
		Choice  string `json:"choice"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	ok := s.agentTurns.Resolve(turnID, body.CallID, body.Approve, body.Choice)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

// agentTurnMessage delivers a free-text nudge and/or a stop request to a running turn — used to steer a
// supervised finish between iterations ("boost saturation" / "stop, keep this one"). Unlike confirm it
// never blocks: the producer drains the mailbox on its own schedule.
// POST /api/agent/turns/{id}/message  {text?, stop?}
func (s *Server) agentTurnMessage(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
		Stop bool   `json:"stop"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	ok := s.agentTurns.PostMessage(turnID, body.Text, body.Stop)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

// sendAgentEvent writes one SSE frame for an agent event (mirrors sendEvent for job events).
func sendAgentEvent(w http.ResponseWriter, f http.Flusher, e agent.Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	f.Flush()
}

// buildAssistMessages converts the wire turns into llm.Messages, prepending the grounding system
// prompt and injecting a measured report into each image-bearing turn. It attaches a 100% centre crop
// only to the latest image turn (to bound tokens) and returns that turn's measurements for the UI.
// Image analysis is soft-fail: an undecodable image is still forwarded, just without a report.
func buildAssistMessages(systemPrompt string, in []agentChatMessage) ([]llm.Message, []pipeline.AssistMeasurement, error) {
	lastImg := -1
	for i, m := range in {
		if len(m.Images) > 0 {
			lastImg = i
		}
	}
	out := make([]llm.Message, 0, len(in)+1)
	out = append(out, llm.Message{Role: "system", Text: systemPrompt})
	var lastMeasurements []pipeline.AssistMeasurement
	for i, m := range in {
		role := m.Role
		if role == "" {
			role = "user"
		}
		msg := llm.Message{Role: role, Text: m.Text}
		var reports []string
		for _, dataURL := range m.Images {
			mime, data, err := parseDataURL(dataURL)
			if err != nil {
				return nil, nil, fmt.Errorf("message %d image: %w", i, err)
			}
			msg.Images = append(msg.Images, llm.InlineImage{Data: data, Mime: mime})
			meas, report, crop, aerr := pipeline.AnalyzeAssistImage(data)
			if aerr != nil {
				continue // soft-fail: forward the raw image without a measured report
			}
			reports = append(reports, report)
			if i == lastImg {
				lastMeasurements = append(lastMeasurements, meas)
				if crop != nil {
					msg.Images = append(msg.Images, llm.InlineImage{Data: crop, Mime: "image/jpeg"})
				}
			}
		}
		if len(reports) > 0 {
			msg.Text = strings.TrimSpace(m.Text + "\n\n" + strings.Join(reports, "\n\n"))
		}
		out = append(out, msg)
	}
	return out, lastMeasurements, nil
}

// parseDataURL decodes a base64 "data:<mime>;base64,<payload>" URL (what FileReader.readAsDataURL
// produces) into its mime type and raw bytes.
func parseDataURL(s string) (mime string, data []byte, err error) {
	if !strings.HasPrefix(s, "data:") {
		return "", nil, fmt.Errorf("not a data URL")
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return "", nil, fmt.Errorf("malformed data URL")
	}
	meta := s[len("data:"):comma] // e.g. "image/png;base64"
	if !strings.Contains(meta, ";base64") {
		return "", nil, fmt.Errorf("data URL must be base64-encoded")
	}
	mime = strings.TrimSuffix(meta, ";base64")
	if mime == "" {
		mime = "image/jpeg"
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s[comma+1:]))
	if err != nil {
		return "", nil, fmt.Errorf("decode base64 image: %w", err)
	}
	return mime, raw, nil
}
