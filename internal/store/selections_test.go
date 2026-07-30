package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSelectionSignature pins the folder-set key: it is the join key between /api/processed
// history rows and saved_selections, so ordering-independence and case-folding are contracts.
func TestSelectionSignature(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"single path", []string{"/Data/M101"}, "/data/m101"},
		{"order independent", []string{"/data/b", "/data/a"}, "/data/a|/data/b"},
		{"case folded", []string{"/Data/M101/Night1", "/DATA/m101/night2"}, "/data/m101/night1|/data/m101/night2"},
		{"duplicates preserved", []string{"/data/a", "/data/a"}, "/data/a|/data/a"},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SelectionSignature(tc.paths))
		})
	}
}

// TestSelectionSignature_MatchesReordered: the same folder-set always maps to the same row.
func TestSelectionSignature_MatchesReordered(t *testing.T) {
	a := SelectionSignature([]string{"/data/M101/n1", "/data/M101/n2"})
	b := SelectionSignature([]string{"/data/m101/N2", "/data/m101/N1"})
	assert.Equal(t, a, b)
}
