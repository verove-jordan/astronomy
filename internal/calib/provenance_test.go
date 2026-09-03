package calib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// setOfFrames writes n dummy frames and returns the set that would be built from them.
func setOfFrames(t *testing.T, dir string, names ...string) inspect.Set {
	t.Helper()
	set := inspect.Set{Key: inspect.SetKey{Type: inspect.Dark, ExposureMs: 60000, Bin: 1}, Count: len(names)}
	for _, n := range names {
		p := filepath.Join(dir, n)
		require.NoError(t, os.WriteFile(p, []byte(n), 0o644))
		set.Frames = append(set.Frames, &inspect.Frame{Path: p})
	}
	return set
}

// The question the library could not answer before: not "does a master with these settings exist?"
// but "was it built from the frames this run supplied?". On a DSLR the two come apart completely —
// no GAIN/OFFSET in the header means every body and every session share one g0o0 key.
func TestBuiltFromSet(t *testing.T) {
	dir := t.TempDir()
	master := &Master{Type: MasterDark, Path: filepath.Join(dir, "master_DARK.fits")}
	mine := setOfFrames(t, dir, "d1.fits", "d2.fits", "d3.fits")
	theirs := setOfFrames(t, dir, "x1.fits", "x2.fits")

	t.Run("no sidecar proves nothing", func(t *testing.T) {
		assert.False(t, builtFromSet(master, mine), "a master that never recorded its pool must be rebuilt")
	})

	writeSig := func(set inspect.Set) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "master_DARK.sig"),
			[]byte(formatPoolSig(poolSignature(framePaths(set.Frames)), len(set.Frames))), 0o644))
	}

	t.Run("the same frames are reusable", func(t *testing.T) {
		writeSig(mine)
		assert.True(t, builtFromSet(master, mine))
	})

	t.Run("someone else's frames are not", func(t *testing.T) {
		writeSig(theirs)
		assert.False(t, builtFromSet(master, mine), "this run brought its own darks; they win")
	})

	t.Run("an edited frame is not", func(t *testing.T) {
		writeSig(mine)
		require.NoError(t, os.WriteFile(mine.Frames[0].Path, []byte("re-exported, different bytes"), 0o644))
		assert.False(t, builtFromSet(master, mine))
	})
}

func TestDropMaster(t *testing.T) {
	// The superseded entry must go, or the light matcher can still pick it: it matches the same
	// settings as the master about to replace it, and list order would decide.
	got := dropMaster(library(), "dark.fits")
	assert.Len(t, got, 2)
	for _, m := range got {
		assert.NotEqual(t, "dark.fits", m.Path)
	}
	assert.Len(t, dropMaster(library(), "absent.fits"), 3, "dropping nothing keeps everything")
}

func TestBorrowedNote(t *testing.T) {
	own := &Master{Path: "/lib/master_DARK.fits", FrameCount: 30}
	borrowed := &Master{Path: "/lib/master_FLAT_RGB_3ms.fits", FrameCount: 30, FromLibrary: true}

	tests := []struct {
		name string
		sel  Selection
		want string
	}{
		{
			// A session that brought its own calibration frames has nothing to declare — Notes are
			// the exceptions, not a log of everything that happened.
			name: "a self-calibrating session says nothing",
			sel:  Selection{Dark: own, Flat: own},
			want: "",
		},
		{
			// The job-629 case: no flats in the folder, so one was taken from the library and divided
			// into every light without a word.
			name: "a borrowed flat is named",
			sel:  Selection{Dark: own, Flat: borrowed},
			want: "from the calibration library, not this session's own frames: flat master_FLAT_RGB_3ms.fits (30 frames)",
		},
		{
			name: "nothing applied, nothing claimed",
			sel:  Selection{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, borrowedNote(tt.sel))
		})
	}
}

// The note has to survive the real path, not just the helper: matchForLight is what the run calls.
func TestMatchForLight_NamesABorrowedMaster(t *testing.T) {
	lib := library()
	for i := range lib {
		lib[i].FromLibrary = true
	}
	sel := MatchForLight(inspect.SetKey{Filter: "L", ExposureMs: 120000, Gain: 139, Offset: 21, Bin: 1, TempBucket: -15}, lib)

	require.NotNil(t, sel.Flat)
	assert.Contains(t, sel.Notes, "from the calibration library, not this session's own frames: "+
		"dark dark.fits (5 frames) · flat flatL.fits (4 frames) · bias bias.fits (8 frames)")
}
