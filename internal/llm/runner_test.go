package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunner_Available(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ok.Close()

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()

	tests := []struct {
		name    string
		runner  *Runner
		wantErr bool
	}{
		{"reachable", New(ok.URL, "m", ""), false},
		{"empty url", New("", "m", ""), true},
		{"nil runner", nil, true},
		{"server error", New(down.URL, "m", ""), true},
		{"unreachable", New("http://127.0.0.1:1/v1", "m", ""), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.runner.Available(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRunner_ChatRequest_TextAndVision(t *testing.T) {
	r := New("http://x/v1", "my-model", "")
	req := r.chatRequest([]Message{
		{Role: "system", Text: "sys"},
		{Role: "user", Text: "look", Image: []byte{1, 2, 3}, ImageMime: "image/png"},
	}, CompleteOptions{Temperature: 0.2, JSON: true})

	assert.Equal(t, "my-model", req["model"])
	assert.Equal(t, 0.2, req["temperature"])
	assert.Equal(t, 800, req["max_tokens"]) // default applied
	assert.Equal(t, map[string]string{"type": "json_object"}, req["response_format"])

	msgs := req["messages"].([]map[string]any)
	require.Len(t, msgs, 2)
	assert.Equal(t, "sys", msgs[0]["content"]) // plain string when no image

	content := msgs[1]["content"].([]map[string]any)
	require.Len(t, content, 2)
	assert.Equal(t, "text", content[0]["type"])
	img := content[1]["image_url"].(map[string]string)
	assert.True(t, strings.HasPrefix(img["url"], "data:image/png;base64,"), "got %q", img["url"])
}

func TestRunner_ChatRequest_NoJSONNoResponseFormat(t *testing.T) {
	req := New("http://x", "m", "").chatRequest([]Message{{Role: "user", Text: "hi"}}, CompleteOptions{})
	_, ok := req["response_format"]
	assert.False(t, ok)
}

func TestRunner_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), `"model":"m"`)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"hello world"}}]}`)
	}))
	defer srv.Close()

	out, err := New(srv.URL, "m", "").Complete(context.Background(),
		[]Message{{Role: "user", Text: "hi"}}, CompleteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello world", out)
}

func TestRunner_Complete_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "m", "").Complete(context.Background(),
		[]Message{{Role: "user", Text: "hi"}}, CompleteOptions{})
	assert.Error(t, err)
}

func TestChatMessages_ImageFormats(t *testing.T) {
	msgs := []Message{{Role: "user", Text: "look", Image: []byte{1, 2, 3}, ImageMime: "image/png"}}

	// OpenAI: image_url is an object {url: <data-url>}.
	oc := chatMessages(msgs, ImageFormatOpenAI)[0]["content"].([]map[string]any)
	require.Len(t, oc, 2)
	assert.Equal(t, "image_url", oc[1]["type"])
	url := oc[1]["image_url"].(map[string]string)["url"]
	assert.True(t, strings.HasPrefix(url, "data:image/png;base64,"), "got %q", url)

	// mlx-vlm: input_image with image_url a bare data-url string.
	mc := chatMessages(msgs, ImageFormatMLXVLM)[0]["content"].([]map[string]any)
	require.Len(t, mc, 2)
	assert.Equal(t, "input_image", mc[1]["type"])
	assert.True(t, strings.HasPrefix(mc[1]["image_url"].(string), "data:image/png;base64,"))
}
