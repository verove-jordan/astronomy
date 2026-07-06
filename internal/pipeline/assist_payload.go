// The chat agent's eyes: package the objective measurements + preview images of a rendered result
// so the ReAct loop can SEE what a run produced and decide parameter changes from the pixels, not
// from a text description.
package pipeline

import (
	"encoding/json"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/llm"
)

// ResultImagePayload measures a result PNG with the supervisor's deterministic metrics and returns
// a grounding report plus the downscaled full frame and native-resolution centre crop as inline
// images. The crop is best-effort (a tiny image may not crop); the full frame is required.
func ResultImagePayload(path string) (string, []llm.InlineImage, error) {
	m, err := measureFinish(path)
	if err != nil {
		return "", nil, fmt.Errorf("measure %s: %w", path, err)
	}
	thumb, err := thumbnailJPEG(path, superviseThumbDim, superviseThumbQuality)
	if err != nil {
		return "", nil, fmt.Errorf("thumbnail %s: %w", path, err)
	}
	imgs := []llm.InlineImage{{Data: thumb, Mime: "image/jpeg"}}
	if crop, cerr := centerCropJPEG(path, superviseCropFrac, superviseCropDim, superviseCropQuality); cerr == nil {
		imgs = append(imgs, llm.InlineImage{Data: crop, Mime: "image/jpeg"})
	}
	mj, _ := json.Marshal(m)
	return "Objective measurements (fractions/levels in 0..1): " + string(mj), imgs, nil
}
