// Package llm drives a host-run, OpenAI-compatible model server (e.g. LM Studio or mlx-vlm on
// Apple Silicon) over HTTP. Like Siril/GraXpert/StarNet it is an optional host tool, invoked never
// vendored: the engine POSTs to the user's own local server (set via ASTRO_LLM_URL) and bundles
// nothing. When the server is absent every caller falls back to the normal pipeline.
//
// The client is deliberately thin — Available + Complete (text, optionally with one inline image for
// vision models). Domain prompts and JSON schemas live with the caller (see internal/pipeline
// supervise), so this package stays generic and reusable.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Image wire-formats for a vision turn. OpenAI Chat uses an image_url object; mlx-vlm's server uses
// a Responses-style input_image string. The server choice is configured (ASTRO_LLM_IMAGE_FORMAT) so
// the same client drives either without code changes.
const (
	ImageFormatOpenAI = "openai"
	ImageFormatMLXVLM = "mlxvlm"
)

// Runner is a client for an OpenAI-compatible chat/completions endpoint with optional vision.
type Runner struct {
	baseURL     string
	model       string
	imageFormat string
	http        *http.Client
}

// New returns a Runner for the given OpenAI-compatible base URL (e.g. http://127.0.0.1:1234/v1),
// model id, and vision image wire-format (ImageFormatOpenAI when empty). An empty base URL yields a
// Runner that reports Unavailable, so "not configured" and "not running" are handled identically.
func New(baseURL, model, imageFormat string) *Runner {
	if imageFormat == "" {
		imageFormat = ImageFormatOpenAI
	}
	return &Runner{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		imageFormat: imageFormat,
		http:        &http.Client{Timeout: 120 * time.Second},
	}
}

// Available reports whether the model server answers. It is a soft check: callers log the error and
// fall back to the normal finish rather than aborting the run.
func (r *Runner) Available(ctx context.Context) error {
	if r == nil || r.baseURL == "" {
		return fmt.Errorf("llm base URL is empty (set ASTRO_LLM_URL)")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("llm server %s unreachable: %w", r.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llm server %s returned %s", r.baseURL, resp.Status)
	}
	return nil
}

// Message is one chat message. When Image is set it is attached to the turn as an inline data-URL
// image (vision models only).
type Message struct {
	Role      string // "system" | "user" | "assistant"
	Text      string
	Image     []byte // optional inline image bytes
	ImageMime string // e.g. "image/jpeg"; defaults to image/jpeg when Image is set
}

// CompleteOptions tune a single completion.
type CompleteOptions struct {
	Temperature float64
	MaxTokens   int  // 0 → a sane default
	JSON        bool // ask the server for a JSON-object response
}

// Complete sends the messages and returns the assistant's reply text.
func (r *Runner) Complete(ctx context.Context, msgs []Message, opts CompleteOptions) (string, error) {
	if err := r.Available(ctx); err != nil {
		return "", err
	}
	body, err := json.Marshal(r.chatRequest(msgs, opts))
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm completion: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm completion returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode llm reply: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// chatRequest builds the OpenAI chat payload (pure, for testing).
func (r *Runner) chatRequest(msgs []Message, opts CompleteOptions) map[string]any {
	maxTok := opts.MaxTokens
	if maxTok == 0 {
		maxTok = 800
	}
	out := map[string]any{
		"model":       r.model,
		"temperature": opts.Temperature,
		"max_tokens":  maxTok,
		"messages":    chatMessages(msgs, r.imageFormat),
	}
	if opts.JSON {
		out["response_format"] = map[string]string{"type": "json_object"}
	}
	return out
}

// chatMessages renders messages into chat content: a plain string when there is no image, or a
// text+image content array for a vision turn (in the configured wire-format).
func chatMessages(msgs []Message, imageFormat string) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		if len(m.Image) == 0 {
			out = append(out, map[string]any{"role": m.Role, "content": m.Text})
			continue
		}
		out = append(out, map[string]any{"role": m.Role, "content": imageContent(m, imageFormat)})
	}
	return out
}

// imageContent renders a vision turn's content blocks in the wire shape the server expects: the
// OpenAI Chat form ({"type":"image_url","image_url":{"url":<data-url>}}) or mlx-vlm's Responses form
// ({"type":"input_image","image_url":<data-url>}).
func imageContent(m Message, imageFormat string) []map[string]any {
	mime := m.ImageMime
	if mime == "" {
		mime = "image/jpeg"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(m.Image)
	text := map[string]any{"type": "text", "text": m.Text}
	if imageFormat == ImageFormatMLXVLM {
		return []map[string]any{text, {"type": "input_image", "image_url": dataURL}}
	}
	return []map[string]any{text, {"type": "image_url", "image_url": map[string]string{"url": dataURL}}}
}
