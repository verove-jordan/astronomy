package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestChatMessages_MultiImage(t *testing.T) {
	// A turn may carry the legacy single Image plus any number of additional Images; all are emitted
	// as content blocks after the text, in order.
	msgs := []Message{{
		Role: "user", Text: "compare",
		Image: []byte{1}, ImageMime: "image/png",
		Images: []InlineImage{{Data: []byte{2}, Mime: "image/jpeg"}, {Data: []byte{3}, Mime: "image/webp"}},
	}}
	content := chatMessages(msgs, ImageFormatOpenAI)[0]["content"].([]map[string]any)
	require.Len(t, content, 4) // text + 3 images
	assert.Equal(t, "text", content[0]["type"])
	for i, wantPrefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/webp;base64,"} {
		url := content[i+1]["image_url"].(map[string]string)["url"]
		assert.Truef(t, strings.HasPrefix(url, wantPrefix), "block %d: got %q", i+1, url)
	}
}

func TestRunner_Models(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"a"},{"id":"b"},{"id":""}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ids, err := New(srv.URL, "m", "").Models(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids) // blank id skipped

	// Unreachable server → error (used as the liveness signal).
	_, err = New("http://127.0.0.1:1/v1", "m", "").Models(context.Background())
	assert.Error(t, err)
}

func TestRunner_Complete_Timeout(t *testing.T) {
	// The /models health check passes, but the completion is slower than the client's timeout, so
	// Complete must return an error. The handler also returns on request-context cancel or a short
	// safety bound, so httptest.Server.Close() never blocks (the client disconnect doesn't always
	// propagate to the server request context).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer srv.Close()

	_, err := New(srv.URL, "m", "").WithTimeout(50*time.Millisecond).
		Complete(context.Background(), []Message{{Role: "user", Text: "hi"}}, CompleteOptions{})
	require.Error(t, err)
}
