package pipeline

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes encodes an image to PNG bytes for the analyzer.
func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestAnalyzeAssistImage_Synthetic(t *testing.T) {
	// A green-biased dark background, a pure-black corner block, and a bright diagonal streak —
	// so the measured stats must show a green cast, black clipping, and a detected trail.
	const n = 240
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			// Green-biased sky with a deterministic dither so the background has non-zero MAD (a
			// perfectly flat frame makes DetectTrail bail — real frames always carry noise).
			d := uint8((x*7 + y*13) % 12)
			img.Set(x, y, color.RGBA{R: 10 + d, G: 30 + d, B: 10 + d, A: 255})
		}
	}
	for y := 0; y < 24; y++ { // pure-black block → black clipping in every channel
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{A: 255})
		}
	}
	for i := 0; i < n; i++ { // ~2px bright diagonal → trail + white clipping
		img.Set(i, i, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		if i+1 < n {
			img.Set(i+1, i, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}

	meas, report, crop, err := AnalyzeAssistImage(pngBytes(t, img))
	require.NoError(t, err)

	assert.Greater(t, meas.GreenCast, 0.0, "green-biased background → positive green_cast")
	assert.Greater(t, meas.BlackClip[0], 0.0, "the black block clips the R channel")
	assert.True(t, meas.Trail, "the diagonal streak is detected as a trail")
	assert.Contains(t, report, "green_cast=+")
	assert.Contains(t, report, "trail=detected")

	require.NotNil(t, crop, "a centre crop is produced")
	_, format, derr := image.Decode(bytes.NewReader(crop))
	require.NoError(t, derr)
	assert.Equal(t, "jpeg", format)
}

func TestAnalyzeAssistImage_FlatGrey(t *testing.T) {
	const n = 120
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			img.Set(x, y, color.RGBA{R: 60, G: 60, B: 60, A: 255})
		}
	}
	meas, report, _, err := AnalyzeAssistImage(pngBytes(t, img))
	require.NoError(t, err)
	assert.Equal(t, 0.0, meas.GreenCast, "equal channels → no cast")
	assert.Equal(t, 0.0, meas.BlackClip[0])
	assert.False(t, meas.Trail)
	assert.Contains(t, report, "green_cast=+0.000")
	assert.Contains(t, report, "trail=none")
}

func TestAnalyzeAssistImage_Undecodable(t *testing.T) {
	meas, report, crop, err := AnalyzeAssistImage([]byte{0, 1, 2, 3})
	require.Error(t, err) // drives the api soft-fail path (raw image forwarded, no report)
	assert.Equal(t, "", report)
	assert.Nil(t, crop)
	assert.Equal(t, AssistMeasurement{}, meas)
}

func TestFormatAssistReport(t *testing.T) {
	m := AssistMeasurement{
		Background:  0.052,
		MedianRGB:   [3]float64{0.071, 0.068, 0.059},
		GreenCast:   0.006,
		BlackClip:   [3]float64{0.021, 0.018, 0.033},
		WhiteClip:   [3]float64{0.004, 0.003, 0.002},
		GradientPct: 8.4,
		Trail:       false,
	}
	want := "AstroStack measurements of this image (objective pixel stats; fractions/levels in 0..1 unless noted — treat as ground truth): " +
		"background=0.052 | median R/G/B=0.071/0.068/0.059 | green_cast=+0.006 | black_clip R/G/B=0.021/0.018/0.033 | " +
		"white_clip R/G/B=0.004/0.003/0.002 | gradient=8.4% | trail=none"
	assert.Equal(t, want, formatAssistReport(m))
}

func TestSharedMenuEmbedded(t *testing.T) {
	// The DRY split must keep the supervisor prompt and the chat prompt on the same knob menu + defects.
	for name, p := range map[string]string{"supervisor": supervisorSystemPrompt, "assist": AssistSystemPrompt} {
		assert.Contains(t, p, tierKnobMenu, name+" embeds the shared knob menu")
		assert.Contains(t, p, defectVocabulary, name+" embeds the shared defect vocabulary")
	}
}

func TestAssistSystemPrompt_Rules(t *testing.T) {
	for _, marker := range []string{
		"Treat these numbers as ground truth", // grounding
		"Do NOT produce a textbook",           // anti-generic
		"SAME LANGUAGE",                       // language mirroring
		"combined_background_ai",              // an on-stack knob token
	} {
		assert.Contains(t, AssistSystemPrompt, marker)
	}
	assert.NotContains(t, AssistSystemPrompt, "‹") // no unresolved concat markers
	assert.False(t, strings.HasPrefix(AssistSystemPrompt, " "))
}
