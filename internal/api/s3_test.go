package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// summarizeRunBytes is the shared core of the runs gallery: it derives a run summary from run.json bytes and
// the local path the run occupies — used both for on-disk runs and for runs read back from the S3 mirror.
func TestSummarizeRunBytes_FromJSON(t *testing.T) {
	data := []byte(`{
		"object": "M101",
		"run_id": "20260701_010203",
		"final": {
			"mode": "LRGB",
			"channels": ["L","R","G","B"],
			"outputs": [
				"/out/M101/20260701_010203/final.tif",
				"/out/M101/20260701_010203/final.png"
			]
		}
	}`)
	p := "/out/M101/20260701_010203/run.json"

	s := summarizeRunBytes(data, p, 1720000000000)

	assert.Equal(t, "M101", s.Object)
	assert.Equal(t, "20260701_010203", s.RunID)
	assert.Equal(t, "LRGB", s.Mode)
	assert.Equal(t, []string{"L", "R", "G", "B"}, s.Channels)
	// The .png output is picked as the gallery preview; its absolute path resolves through S3-fallback serving.
	assert.Equal(t, "/out/M101/20260701_010203/final.png", s.FinalPreview)
	assert.Equal(t, p, s.RunJSON)
	assert.Equal(t, "/out/M101/20260701_010203", s.Dir)
	assert.Equal(t, int64(1720000000000), s.CreatedAtMs)
}

func TestSummarizeRunBytes_EmptyFallsBackToPath(t *testing.T) {
	// Unreadable/empty run.json: object + run id are derived from the path, the mtime is preserved, and
	// there is no preview (guards the S3 fallback when GetBytes fails).
	s := summarizeRunBytes(nil, "/out/NGC7000/run_x/run.json", 42)

	assert.Equal(t, "NGC7000", s.Object)
	assert.Equal(t, "run_x", s.RunID)
	assert.Equal(t, int64(42), s.CreatedAtMs)
	assert.Empty(t, s.FinalPreview)
	assert.Empty(t, s.Mode)
}

// cleanRel must confine every transfer rel_path to within its namespace — a leading ".." is absorbed by the
// anchoring "/", so it can never escape; only empty/root-resolving inputs are rejected.
func TestCleanRel(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"M101", "M101", true},
		{"M101/L", "M101/L", true},
		{"a/b/../c", "a/c", true},
		{"./M101", "M101", true},
		{`M101\L`, "M101/L", true}, // backslashes normalized to forward slashes
		{"../etc", "etc", true},    // leading ".." absorbed → confined, never escapes
		{"../../secret", "secret", true},
		{"", "", false},
		{".", "", false},
		{"..", "", false},      // resolves to root → rejected
		{"M101/..", "", false}, // cancels out to root → rejected
	}
	for _, tc := range cases {
		got, ok := cleanRel(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		if tc.ok {
			assert.Equal(t, tc.want, got, tc.in)
		}
	}
}
