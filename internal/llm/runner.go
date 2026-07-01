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

// defaultCompleteTimeout bounds a single completion when the caller doesn't set one via WithTimeout.
// Generous because a vision generation over a large image can take minutes; WithTimeout(0) disables it.
const defaultCompleteTimeout = 30 * time.Minute

// Runner is a client for an OpenAI-compatible chat/completions endpoint with optional vision.
type Runner struct {
	baseURL         string
	model           string
	imageFormat     string
	completeTimeout time.Duration
	http            *http.Client
}

// New returns a Runner for the given OpenAI-compatible base URL (e.g. http://127.0.0.1:1234/v1),
// model id, and vision image wire-format (ImageFormatOpenAI when empty). An empty base URL yields a
// Runner that reports Unavailable, so "not configured" and "not running" are handled identically.
func New(baseURL, model, imageFormat string) *Runner {
	if imageFormat == "" {
		imageFormat = ImageFormatOpenAI
	}
	return &Runner{
		baseURL:         strings.TrimRight(baseURL, "/"),
		model:           model,
		imageFormat:     imageFormat,
		completeTimeout: defaultCompleteTimeout,
		// No client-wide Timeout: Available/Models bound themselves with a short context, and Complete
		// applies completeTimeout via context — so a slow but healthy vision generation is never cut
		// mid-flight (a fixed http.Client.Timeout aborts it "while awaiting headers" no matter what).
		http: &http.Client{},
	}
}

// WithTimeout sets the maximum wall-clock for a single Complete call (0 or negative → no limit, bounded
// only by the caller's context). Returns the runner for chaining.
func (r *Runner) WithTimeout(d time.Duration) *Runner {
	r.completeTimeout = d
	return r
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

// Models lists the model ids the server advertises (OpenAI-compatible GET /models, served by both
// mlx-vlm and Ollama). It doubles as a liveness probe — a non-nil error means the server is not
// reachable — and feeds the AstroAgent model picker.
func (r *Runner) Models(ctx context.Context) ([]string, error) {
	if r == nil || r.baseURL == "" {
		return nil, fmt.Errorf("llm base URL is empty (set ASTRO_LLM_URL)")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm server %s unreachable: %w", r.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm server %s returned %s", r.baseURL, resp.Status)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// InlineImage is one image attached to a chat turn (vision models only). Data is the raw bytes; Mime
// is e.g. "image/png" and defaults to image/jpeg when empty.
type InlineImage struct {
	Data []byte
	Mime string
}

// Message is one chat message. When Image and/or Images are set they are attached to the turn as
// inline data-URL images (vision models only). Image is the legacy single-image field kept for
// existing callers; Images carries any number of additional images for a multi-image turn.
type Message struct {
	Role      string // "system" | "user" | "assistant"
	Text      string
	Image     []byte // optional single inline image (rendered before Images)
	ImageMime string // e.g. "image/jpeg"; defaults to image/jpeg when Image is set
	Images    []InlineImage
}

// CompleteOptions tune a single completion.
type CompleteOptions struct {
	Temperature float64
	MaxTokens   int  // 0 → a sane default
	JSON        bool // ask the server for a JSON-object response
}

// Complete sends the messages and returns the assistant's reply text.
func (r *Runner) Complete(ctx context.Context, msgs []Message, opts CompleteOptions) (string, error) {
	if r.completeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.completeTimeout)
		defer cancel()
	}
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
		if len(m.Image) == 0 && len(m.Images) == 0 {
			out = append(out, map[string]any{"role": m.Role, "content": m.Text})
			continue
		}
		out = append(out, map[string]any{"role": m.Role, "content": imageContent(m, imageFormat)})
	}
	return out
}

// imageContent renders a vision turn's content blocks: a leading text block, then the legacy single
// Image (if set) followed by each of Images, all in the configured wire-format.
func imageContent(m Message, imageFormat string) []map[string]any {
	blocks := []map[string]any{{"type": "text", "text": m.Text}}
	if len(m.Image) > 0 {
		blocks = append(blocks, imageBlock(m.Image, m.ImageMime, imageFormat))
	}
	for _, img := range m.Images {
		blocks = append(blocks, imageBlock(img.Data, img.Mime, imageFormat))
	}
	return blocks
}

// imageBlock renders one inline image as a content block in the wire shape the server expects: the
// OpenAI Chat form ({"type":"image_url","image_url":{"url":<data-url>}}) or mlx-vlm's Responses form
// ({"type":"input_image","image_url":<data-url>}).
func imageBlock(data []byte, mime, imageFormat string) map[string]any {
	if mime == "" {
		mime = "image/jpeg"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	if imageFormat == ImageFormatMLXVLM {
		return map[string]any{"type": "input_image", "image_url": dataURL}
	}
	return map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}}
}
