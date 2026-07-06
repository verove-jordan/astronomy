package turns

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSteerMailbox_DrainClearsNudgesStickyStop(t *testing.T) {
	h := NewSessions()
	id := h.Start()

	require.True(t, h.PostMessage(id, "boost saturation", false))
	require.True(t, h.PostMessage(id, "   ", false)) // blank text is ignored
	require.True(t, h.PostMessage(id, "less sharpening", true))

	texts, stop := h.DrainMessages(id)
	assert.Equal(t, []string{"boost saturation", "less sharpening"}, texts)
	assert.True(t, stop)

	// A second drain clears the nudges, but the stop stays sticky so a re-polling loop still sees it.
	texts, stop = h.DrainMessages(id)
	assert.Empty(t, texts)
	assert.True(t, stop)
}

func TestSteerMailbox_UnknownTurn(t *testing.T) {
	h := NewSessions()
	assert.False(t, h.PostMessage("nope", "hi", false))
	texts, stop := h.DrainMessages("nope")
	assert.Nil(t, texts)
	assert.False(t, stop)
}

func TestPublishSubscribe_BacklogCarriesPreview(t *testing.T) {
	h := NewSessions()
	id := h.Start()
	h.Publish(id, Event{Kind: "thinking", Step: 1, Text: "pass 1", Preview: "/out/final_iter0.png"})
	h.Finish(id)

	backlog, _, cancel, ok := h.Subscribe(id)
	require.True(t, ok)
	defer cancel()
	require.GreaterOrEqual(t, len(backlog), 2)
	assert.Equal(t, "thinking", backlog[0].Kind)
	assert.Equal(t, "/out/final_iter0.png", backlog[0].Preview)
	assert.Equal(t, "done", backlog[len(backlog)-1].Kind)

	// Preview survives the JSON round-trip the SSE layer performs on each event.
	b, err := json.Marshal(backlog[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"preview":"/out/final_iter0.png"`)
	var got Event
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "/out/final_iter0.png", got.Preview)
}

func TestSubscribe_UnknownTurn(t *testing.T) {
	h := NewSessions()
	_, _, _, ok := h.Subscribe("nope")
	assert.False(t, ok)
}

func TestAwaitResolve_DeliversChoice(t *testing.T) {
	h := NewSessions()
	id := h.Start()

	type answer struct {
		approve bool
		choice  string
		ok      bool
	}
	got := make(chan answer, 1)
	go func() {
		approve, choice, ok := h.Await(context.Background(), id, "c1")
		got <- answer{approve, choice, ok}
	}()

	// Resolve returns false until the waiter has registered its pending channel; retry until it lands.
	require.Eventually(t, func() bool { return h.Resolve(id, "c1", true, "Proceed") },
		time.Second, 5*time.Millisecond)

	a := <-got
	assert.True(t, a.ok)
	assert.True(t, a.approve)
	assert.Equal(t, "Proceed", a.choice)
}

func TestAwait_UnknownTurn(t *testing.T) {
	h := NewSessions()
	_, _, ok := h.Await(context.Background(), "nope", "c1")
	assert.False(t, ok)
}
