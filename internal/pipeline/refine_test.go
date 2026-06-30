package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReconstructChannelsFromDisk(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// L has both aligned + master (aligned wins); R has only the unaligned master; B has neither.
	touch("aligned_L.fits")
	touch("master_L.fits")
	touch("master_R.fits")

	prior := []ChannelResult{{Filter: "L"}, {Filter: "R"}, {Filter: "B"}, {Filter: ""}}
	got := reconstructChannelsFromDisk(dir, prior)

	assert.Equal(t, "aligned_L", got["L"]) // aligned preferred over master
	assert.Equal(t, "master_R", got["R"])  // falls back to the unaligned master
	_, hasB := got["B"]
	assert.False(t, hasB) // no file on disk → skipped
	assert.Len(t, got, 2)
}
