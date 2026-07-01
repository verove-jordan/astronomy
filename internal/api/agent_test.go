package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// modelServerOpts configures the fake OpenAI-compatible model server.
type modelServerOpts struct {
	modelsStatus int    // status for GET /models (default 200)
	chatStatus   int    // status for POST /chat/completions (default 200)
	reply        string // assistant content to return (default "ok reply")
}

// capturedChat records the last /chat/completions request body the fake server saw.
type capturedChat struct{ body []byte }

func fakeModelServer(t *testing.T, o modelServerOpts) (*httptest.Server, *capturedChat) {
	t.Helper()
	if o.modelsStatus == 0 {
		o.modelsStatus = http.StatusOK
	}
	if o.chatStatus == 0 {
		o.chatStatus = http.StatusOK
	}
	if o.reply == "" {
		o.reply = "ok reply"
	}
	cap := &capturedChat{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if o.modelsStatus != http.StatusOK {
				w.WriteHeader(o.modelsStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"model-a"},{"id":"model-b"}]}`)
		case "/chat/completions":
			cap.body, _ = io.ReadAll(r.Body)
			if o.chatStatus != http.StatusOK {
				w.WriteHeader(o.chatStatus)
				_, _ = io.WriteString(w, `{"error":"boom"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+strconv.Quote(o.reply)+`}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func doReq(s *Server, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestAgentStatus(t *testing.T) {
	up, _ := fakeModelServer(t, modelServerOpts{})
	down, _ := fakeModelServer(t, modelServerOpts{modelsStatus: http.StatusInternalServerError})

	tests := []struct {
		name        string
		cfg         *config.Config
		wantRunning bool
		wantModels  int
		wantModel   string
	}{
		{"running lists models", &config.Config{LLMBaseURL: up.URL, LLMModel: "model-a"}, true, 2, "model-a"},
		{"down on server error", &config.Config{LLMBaseURL: down.URL, LLMModel: "model-a"}, false, 0, "model-a"},
		{"down when unconfigured", &config.Config{LLMBaseURL: "", LLMModel: ""}, false, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doReq(&Server{cfg: tt.cfg}, http.MethodGet, "/api/agent/status", "")
			require.Equal(t, http.StatusOK, rec.Code) // never fails the request
			var resp struct {
				Running bool     `json:"running"`
				Model   string   `json:"model"`
				Models  []string `json:"models"`
			}
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
			assert.Equal(t, tt.wantRunning, resp.Running)
			assert.Len(t, resp.Models, tt.wantModels)
			assert.Equal(t, tt.wantModel, resp.Model)
			assert.NotNil(t, resp.Models, "models is [] not null so the UI can iterate")
		})
	}
}

func TestAgentChat_InjectsModelAndReplies(t *testing.T) {
	srv, cap := fakeModelServer(t, modelServerOpts{reply: "pong"})
	s := &Server{cfg: &config.Config{LLMBaseURL: srv.URL, LLMModel: "cfg-model"}}

	rec := doReq(s, http.MethodPost, "/api/agent/chat", `{"messages":[{"role":"user","text":"ping"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Reply string `json:"reply"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "pong", resp.Reply)
	assert.Contains(t, string(cap.body), `"model":"cfg-model"`, "server injects the configured model id")
}

func TestAgentChat_ModelOverride(t *testing.T) {
	srv, cap := fakeModelServer(t, modelServerOpts{})
	s := &Server{cfg: &config.Config{LLMBaseURL: srv.URL, LLMModel: "cfg-model"}}

	rec := doReq(s, http.MethodPost, "/api/agent/chat",
		`{"model":"override-model","messages":[{"role":"user","text":"hi"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, string(cap.body), `"model":"override-model"`)
	assert.NotContains(t, string(cap.body), "cfg-model")
}

func TestAgentChat_ForwardsImage(t *testing.T) {
	srv, cap := fakeModelServer(t, modelServerOpts{})
	s := &Server{cfg: &config.Config{LLMBaseURL: srv.URL, LLMModel: "m"}}

	// base64 of bytes {0,1,2,3} is "AAECAw==".
	rec := doReq(s, http.MethodPost, "/api/agent/chat",
		`{"messages":[{"role":"user","text":"what is this?","images":["data:image/png;base64,AAECAw=="]}]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, string(cap.body), "image_url", "image is forwarded as a vision content block")
	assert.Contains(t, string(cap.body), "data:image/png;base64,AAECAw==")
	// The 4-byte "image" can't be decoded, so the measurement is soft-failed — the raw image is still sent.
	assert.NotContains(t, string(cap.body), "green_cast=", "no measured report for an undecodable image")
}

func TestAgentChat_Errors(t *testing.T) {
	okSrv, _ := fakeModelServer(t, modelServerOpts{})
	errSrv, _ := fakeModelServer(t, modelServerOpts{chatStatus: http.StatusInternalServerError})

	tests := []struct {
		name     string
		cfg      *config.Config
		body     string
		wantCode int
	}{
		{"upstream error propagates", &config.Config{LLMBaseURL: errSrv.URL, LLMModel: "m"},
			`{"messages":[{"role":"user","text":"hi"}]}`, http.StatusInternalServerError},
		{"empty model rejected before call", &config.Config{LLMBaseURL: okSrv.URL, LLMModel: ""},
			`{"messages":[{"role":"user","text":"hi"}]}`, http.StatusBadRequest},
		{"missing messages", &config.Config{LLMBaseURL: okSrv.URL, LLMModel: "m"},
			`{"messages":[]}`, http.StatusBadRequest},
		{"invalid body", &config.Config{LLMBaseURL: okSrv.URL, LLMModel: "m"},
			`not json`, http.StatusBadRequest},
		{"bad image data URL", &config.Config{LLMBaseURL: okSrv.URL, LLMModel: "m"},
			`{"messages":[{"role":"user","text":"x","images":["http://nope"]}]}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doReq(&Server{cfg: tt.cfg}, http.MethodPost, "/api/agent/chat", tt.body)
			assert.Equal(t, tt.wantCode, rec.Code)
		})
	}
}

func TestParseDataURL(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantMime string
		wantData []byte
		wantErr  bool
	}{
		{"png", "data:image/png;base64,AAECAw==", "image/png", []byte{0, 1, 2, 3}, false},
		{"default mime", "data:;base64,AAECAw==", "image/jpeg", []byte{0, 1, 2, 3}, false},
		{"not a data url", "http://x/y.png", "", nil, true},
		{"missing base64 marker", "data:image/png,hello", "", nil, true},
		{"bad base64", "data:image/png;base64,@@@@", "", nil, true},
		{"no comma", "data:image/png;base64", "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, data, err := parseDataURL(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMime, mime)
			assert.Equal(t, tt.wantData, data)
		})
	}
}

// validPNGDataURL builds a real (decodable) PNG data URL so the grounding path actually measures it.
func validPNGDataURL(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 40, B: 20, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestAgentChat_GroundsWithSystemPromptAndMeasurements(t *testing.T) {
	srv, cap := fakeModelServer(t, modelServerOpts{reply: "ok"})
	s := &Server{cfg: &config.Config{LLMBaseURL: srv.URL, LLMModel: "m"}}

	body := `{"messages":[{"role":"user","text":"defauts ?","images":["` + validPNGDataURL(t) + `"]}]}`
	rec := doReq(s, http.MethodPost, "/api/agent/chat", body)
	require.Equal(t, http.StatusOK, rec.Code)

	sent := string(cap.body)
	assert.Contains(t, sent, `"role":"system"`, "a grounding system prompt is prepended")
	assert.Contains(t, sent, "You are AstroAgent", "the system message is AssistSystemPrompt")
	assert.Contains(t, sent, "green_cast=", "the measured report is injected into the image turn")

	var resp struct {
		Reply        string `json:"reply"`
		Measurements []struct {
			Background float64 `json:"background"`
		} `json:"measurements"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "ok", resp.Reply)
	require.Len(t, resp.Measurements, 1, "the latest image's measurements are returned to the UI")
}

func TestAgentChat_TextOnlyHasSystemPromptNoMeasurements(t *testing.T) {
	srv, cap := fakeModelServer(t, modelServerOpts{})
	s := &Server{cfg: &config.Config{LLMBaseURL: srv.URL, LLMModel: "m"}}

	rec := doReq(s, http.MethodPost, "/api/agent/chat", `{"messages":[{"role":"user","text":"hello"}]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	sent := string(cap.body)
	assert.Contains(t, sent, "You are AstroAgent", "system prompt present even without images")
	assert.NotContains(t, sent, "green_cast=", "no report without an image")

	var resp struct {
		Measurements []any `json:"measurements"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Empty(t, resp.Measurements)
}
