package skycat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCatalog_IsStarCluster: globular ("GCl") and open ("OCl") clusters are pure star fields the pipeline
// routes to the gentle star-field finish; a cluster WITH nebulosity ("Cl+N"), galaxies and nebulae are not,
// and an unknown name is not (→ the default galaxy/nebula recipe). Resolves via the Messier alias and the
// bare NGC/IC name, case-insensitively.
func TestCatalog_IsStarCluster(t *testing.T) {
	cat := Load(t.TempDir()) // embedded snapshot, OpenNGC overlay applied

	tests := []struct {
		name string
		want bool
	}{
		{"M92", true},          // NGC6341, GCl — the reported globular (via Messier alias)
		{"NGC6341", true},      // same, via the NGC name
		{"ngc6205", true},      // M13, GCl — case-insensitive
		{"NGC6705", true},      // M11, OCl — an open cluster
		{"NGC2682", true},      // M67, OCl
		{"IC348", false},       // Cl+N — a cluster WITH nebulosity wants the nebula recipe, not the star-field one
		{"M31", false},         // galaxy
		{"NGC7000", false},     // HII / emission nebula
		{"NGC99999999", false}, // unknown → default recipe
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cat.IsStarCluster(tt.name))
		})
	}
}
