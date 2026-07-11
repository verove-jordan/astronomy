package align

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawStarListRows counts the data rows of an embedded list CSV so the tests can prove the
// loader skipped nothing (every hand-controller row resolves against brightstars.csv).
func rawStarListRows(t *testing.T, path string) []string {
	t.Helper()
	data, err := starListsFS.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Greater(t, len(lines), 1, "file should have a header plus rows")
	return lines[1:]
}

func TestStarLists_Integrity(t *testing.T) {
	byMag := make(map[string]float64, len(catalog))
	for _, s := range catalog {
		byMag[strings.ToLower(s.Name)] = s.Mag
	}

	tests := []struct {
		key  string
		path string
		min  int
	}{
		{"celestron", "starlists/celestron.csv", 80},
		{"synscan", "starlists/synscan.csv", 120},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			list := starLists[tt.key]
			require.NotEmpty(t, list)
			assert.GreaterOrEqual(t, len(list), tt.min)

			// Zero defensive skips: every raw row made it into the parsed map.
			rows := rawStarListRows(t, tt.path)
			assert.Len(t, list, len(rows), "every CSV row must resolve against brightstars.csv")

			// No duplicate HC labels, and every member is selectable within the profiles' MagLimit.
			seenHC := map[string]bool{}
			for catalogKey, hc := range list {
				mag, ok := byMag[catalogKey]
				require.True(t, ok, "catalog key %q must exist in brightstars.csv", catalogKey)
				assert.LessOrEqual(t, mag, 3.5, "%q is fainter than the profile MagLimit", catalogKey)
				assert.False(t, seenHC[strings.ToLower(hc)], "duplicate HC label %q", hc)
				seenHC[strings.ToLower(hc)] = true
			}
		})
	}
}

func TestStarLists_Aliases(t *testing.T) {
	tests := []struct {
		list    string
		catalog string
		wantHC  string
	}{
		{"celestron", "Rigil Kentaurus", "Alpha Centauri"},
		{"celestron", "Phecda", "Phad"},
		{"celestron", "Gamma Cassiopeiae", "Tsih"},
		{"celestron", "Elnath", "El Nath"},
		{"synscan", "Zubeneschamali", "Zubeneshamali"},
		{"synscan", "Acrab", "Graffias"},
		{"synscan", "Hatysa", "Nair Saif"},
		{"synscan", "Gamma Velorum", "Regor"},
	}
	for _, tt := range tests {
		t.Run(tt.list+"/"+tt.wantHC, func(t *testing.T) {
			assert.Equal(t, tt.wantHC, hcLabel(tt.list, tt.catalog))
			assert.True(t, inStarList(tt.list, tt.catalog))
			assert.True(t, inStarList(tt.list, strings.ToUpper(tt.catalog)), "matching is case-insensitive")
		})
	}
}

func TestStarLists_UnknownKeyAndMisses(t *testing.T) {
	assert.True(t, inStarList("", "Wasat"), "empty list key means unrestricted")
	assert.Equal(t, "", hcLabel("", "Vega"))
	assert.False(t, inStarList("bogus", "Vega"))
	assert.Equal(t, "", hcLabel("bogus", "Vega"))
	// Bayer-style catalog entries absent from the Celestron hand control must be filtered out.
	assert.False(t, inStarList("celestron", "Wasat"))
	assert.False(t, inStarList("celestron", "Delta Pavonis"))
}
