package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/pipeline"
)

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

// agentChat proxies a prompt+images conversation to the configured local model and returns the reply
// plus the objective measurements of the latest image(s). It prepends a grounding system prompt
// (pipeline.AssistSystemPrompt + optional cfg.LLMAssistPromptExtra) and injects a measured "ground
// truth" report into each image turn, so the critique is factual and on-stack rather than generic. The
// model id is injected server-side (request override or cfg.LLMModel) so an empty id can never reach
// the server — mlx-vlm 500s on a blank model. POST /api/agent/chat
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
	sys := pipeline.AssistSystemPrompt
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
	runner := llm.New(s.cfg.LLMBaseURL, model, s.cfg.LLMImageFormat).WithTimeout(s.cfg.LLMTimeout)
	reply, err := runner.Complete(r.Context(), msgs, llm.CompleteOptions{
		Temperature: body.Temperature,
		MaxTokens:   body.MaxTokens, // 0 → the client's sane default
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply, "measurements": measurements})
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
