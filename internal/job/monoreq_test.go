package job

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The RunRequest wire contract for the mono side-outputs must match exactly what the frontend sends
// (stores/jobs.ts: body.output_luminance / body.output_mono_stack): output_luminance is a tri-state
// (*bool — absent inherits the mode default, present forces on/off) and output_mono_stack a plain
// opt-in flag. A tag typo here would silently drop the toggle.
func TestRunRequest_MonoOutputFields(t *testing.T) {
	t.Run("both present forces luminance off and mono stack on", func(t *testing.T) {
		var r RunRequest
		require.NoError(t, json.Unmarshal([]byte(
			`{"path":"input/M31","mode":"deepsky","output_luminance":false,"output_mono_stack":true}`), &r))
		require.NotNil(t, r.OutputLuminanceMono)
		assert.False(t, *r.OutputLuminanceMono)
		assert.True(t, r.OutputMonoStack)
	})
	t.Run("omitted leaves the tri-state nil and the flag off", func(t *testing.T) {
		var r RunRequest
		require.NoError(t, json.Unmarshal([]byte(`{"path":"input/M31","mode":"deepsky"}`), &r))
		assert.Nil(t, r.OutputLuminanceMono, "absent luminance flag inherits the mode default (on for deepsky/nebula)")
		assert.False(t, r.OutputMonoStack, "absent mono-stack flag defaults off")
	})
	t.Run("luminance true is carried through", func(t *testing.T) {
		var r RunRequest
		require.NoError(t, json.Unmarshal([]byte(`{"output_luminance":true}`), &r))
		require.NotNil(t, r.OutputLuminanceMono)
		assert.True(t, *r.OutputLuminanceMono)
	})
}
