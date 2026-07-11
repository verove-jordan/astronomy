package fits

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// ramp builds a deterministic w×h×c image whose pixels are distinct floats, so a banded read can be
// checked value-for-value against the full ReadImage.
func ramp(w, h, c int) *Image {
	im := NewImage(w, h, c)
	for ch := 0; ch < c; ch++ {
		for i := range im.Pix[ch] {
			im.Pix[ch][i] = float32(ch*1000) + float32(i)*0.5
		}
	}
	return im
}

func TestReadPlaneBand_MatchesReadImage(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		w, h, c int
	}{
		{"mono", 6, 5, 1},
		{"rgb", 7, 8, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".fits")
			require.NoError(t, ramp(tt.w, tt.h, tt.c).WriteFITS(path))
			want, err := ReadImage(path)
			require.NoError(t, err)

			f, err := Open(path)
			require.NoError(t, err)

			bands := [][2]int{{0, tt.h}, {1, tt.h - 1}, {tt.h / 2, tt.h/2 + 1}}
			for ch := 0; ch < tt.c; ch++ {
				for _, b := range bands {
					got, err := f.ReadPlaneBand(ch, b[0], b[1])
					require.NoError(t, err, "ch %d band %v", ch, b)
					require.Len(t, got, (b[1]-b[0])*tt.w)
					exp := want.Pix[ch][b[0]*tt.w : b[1]*tt.w]
					assert.Equal(t, exp, got, "ch %d band %v", ch, b)
				}
			}
		})
	}
}

func TestReadPlaneBand_Errors(t *testing.T) {
	dir := t.TempDir()

	// Out-of-range band / channel on a valid float32 file.
	fpath := filepath.Join(dir, "ok.fits")
	require.NoError(t, ramp(4, 4, 1).WriteFITS(fpath))
	f, err := Open(fpath)
	require.NoError(t, err)
	cases := []struct {
		name       string
		ch, y0, y1 int
	}{
		{"chan too high", 1, 0, 4},
		{"chan negative", -1, 0, 4},
		{"y1 beyond height", 0, 0, 5},
		{"y0 negative", 0, -1, 2},
		{"empty band", 0, 2, 2},
		{"inverted band", 0, 3, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.ReadPlaneBand(c.ch, c.y0, c.y1)
			assert.Error(t, err)
		})
	}

	// A 16-bit file violates the -32/0/1 contract.
	t.Run("not float32", func(t *testing.T) {
		p16 := fitstest.Write(t, dir, "i16.fits", 4, 4, 1000, nil)
		f16, err := Open(p16)
		require.NoError(t, err)
		_, err = f16.ReadPlaneBand(0, 0, 4)
		assert.Error(t, err)
	})
}
