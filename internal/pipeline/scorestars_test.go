package pipeline

import (
	"testing"

	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestScoreFinishMode_StarColorSpreadPenalty(t *testing.T) {
	// A neutral, well-exposed finish (no clip, no cast, sky on target) so only the star-colour-spread
	// term moves the score.
	neutral := func(spread float64) finishMetrics {
		return finishMetrics{Background: 0.06, StarColorSpread: spread}
	}
	varied := scoreFinishMode(mode.Deepsky, neutral(0.10), 0.06, finishMetrics{})
	flattened := scoreFinishMode(mode.Deepsky, neutral(0.01), 0.06, finishMetrics{})
	noCores := scoreFinishMode(mode.Deepsky, neutral(0), 0.06, finishMetrics{})

	if flattened >= varied {
		t.Errorf("a flattened star field (%.2f) should score below a varied one (%.2f)", flattened, varied)
	}
	if noCores != varied {
		t.Errorf("spread=0 (no cores sampled) must not be penalised: got %.2f, varied %.2f", noCores, varied)
	}
}
