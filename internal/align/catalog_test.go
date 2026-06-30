package align

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalog_Integrity(t *testing.T) {
	stars := Catalog()
	require.GreaterOrEqual(t, len(stars), 150, "the embedded catalog must be dense enough to align anywhere")

	seen := map[string]bool{}
	var north, south int
	for _, s := range stars {
		require.NotEmpty(t, s.Name)
		key := strings.ToLower(s.Name)
		assert.Falsef(t, seen[key], "duplicate star %q", s.Name)
		seen[key] = true

		assert.GreaterOrEqualf(t, s.RADeg, 0.0, "%s RA", s.Name)
		assert.Lessf(t, s.RADeg, 360.0, "%s RA", s.Name)
		assert.GreaterOrEqualf(t, s.DecDeg, -90.0, "%s Dec", s.Name)
		assert.LessOrEqualf(t, s.DecDeg, 90.0, "%s Dec", s.Name)
		// Sigma Octantis (the south pole star, mag ~5.5) is the one intentionally faint entry: it is
		// the only naked-eye marker of the south celestial pole, kept as a pole reference even though
		// the selection mag limit (~3.5) never auto-picks it. Every other catalog star must be bright.
		if !strings.EqualFold(s.Name, "Sigma Octantis") {
			assert.LessOrEqualf(t, s.Mag, 4.0, "%s should be a naked-eye star", s.Name)
		}

		if s.DecDeg > 40 {
			north++
		}
		if s.DecDeg < -40 {
			south++
		}
	}
	assert.Positive(t, north, "catalog must cover the far northern sky")
	assert.Positive(t, south, "catalog must cover the far southern sky")
}

func TestCatalog_HasAnchorStars(t *testing.T) {
	// A handful of unmistakable bright stars must be present so any sky/hemisphere is coverable.
	for _, name := range []string{"Vega", "Sirius", "Arcturus", "Capella", "Canopus", "Achernar", "Acrux"} {
		assert.Truef(t, hasStar(name), "expected %q in the catalog", name)
	}
}

func hasStar(name string) bool {
	for _, s := range catalog {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}
