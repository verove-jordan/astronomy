package rawconv

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundLongEdge(t *testing.T) {
	tests := []struct {
		name          string
		w, h, maxEdge int
		wantW, wantH  int
	}{
		{"landscape scaled to long edge", 4000, 3000, 1000, 1000, 750},
		{"portrait scaled to long edge", 3000, 4000, 1000, 750, 1000},
		{"already smaller → unchanged", 800, 600, 1000, 800, 600},
		{"maxEdge<=0 → unchanged", 4000, 3000, 0, 4000, 3000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boundLongEdge(image.NewRGBA(image.Rect(0, 0, tt.w, tt.h)), tt.maxEdge)
			assert.Equal(t, tt.wantW, got.Bounds().Dx())
			assert.Equal(t, tt.wantH, got.Bounds().Dy())
		})
	}
}

// writeFile creates a non-empty file at dir/name and returns its path.
func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	return p
}

func TestPrepareTIFF_NativePassthrough(t *testing.T) {
	src := t.TempDir()
	// Mixed case extensions; order must be preserved and names sequential + lowercased.
	srcs := []string{
		writeFile(t, src, "first.png"),
		writeFile(t, src, "second.JPG"),
		writeFile(t, src, "third.tif"),
	}
	dst := filepath.Join(t.TempDir(), "seq")

	var progressed int
	out, warn, err := PrepareTIFF(context.Background(), srcs, dst, func(i, n int, name string) {
		progressed++
	})

	require.NoError(t, err)
	assert.Empty(t, warn)
	assert.Equal(t, len(srcs), progressed)
	require.Equal(t, []string{
		filepath.Join(dst, "frame_00001.png"),
		filepath.Join(dst, "frame_00002.jpg"),
		filepath.Join(dst, "frame_00003.tif"),
	}, out)

	// Native stills are symlinked to the originals, not copied/transcoded.
	for i, link := range out {
		target, lerr := os.Readlink(link)
		require.NoError(t, lerr)
		assert.Equal(t, srcs[i], target)
	}
}

func TestPrepareTIFF_NoSourcesIsError(t *testing.T) {
	out, warn, err := PrepareTIFF(context.Background(), nil, filepath.Join(t.TempDir(), "seq"), nil)
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Empty(t, warn)
}

func TestPrepareTIFF_UndecodableRawWarnsAndFails(t *testing.T) {
	// A ".dng" that is not real raw data: sips (or its absence) fails, the frame is skipped with a
	// warning, and since nothing else succeeds PrepareTIFF returns an error.
	src := t.TempDir()
	bad := writeFile(t, src, "bogus.dng")
	out, warn, err := PrepareTIFF(context.Background(), []string{bad}, filepath.Join(t.TempDir(), "seq"), nil)
	require.Error(t, err)
	assert.Nil(t, out)
	require.Len(t, warn, 1)
	assert.Contains(t, warn[0], "bogus.dng")
}

func TestPrepareTIFF_PartialSuccessKeepsGoodFrames(t *testing.T) {
	src := t.TempDir()
	srcs := []string{
		writeFile(t, src, "ok.png"),    // native → symlinked
		writeFile(t, src, "bogus.dng"), // undecodable → warning, skipped
	}
	dst := filepath.Join(t.TempDir(), "seq")
	out, warn, err := PrepareTIFF(context.Background(), srcs, dst, nil)

	require.NoError(t, err) // at least one frame prepared
	require.Equal(t, []string{filepath.Join(dst, "frame_00001.png")}, out)
	require.Len(t, warn, 1)
	assert.Contains(t, warn[0], "bogus.dng")
}
