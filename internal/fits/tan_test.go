package fits

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWCS_Rejections(t *testing.T) {
	tests := []struct {
		name  string
		cards map[string]string
	}{
		{"nil header", nil},
		{"missing CRVAL", map[string]string{
			"CRPIX1": "100", "CRPIX2": "100", "CDELT1": "0.0003", "CDELT2": "0.0003",
		}},
		{"missing CRPIX", map[string]string{
			"CRVAL1": "180", "CRVAL2": "0", "CDELT1": "0.0003", "CDELT2": "0.0003",
		}},
		{"no matrix at all", map[string]string{
			"CRVAL1": "180", "CRVAL2": "0", "CRPIX1": "100", "CRPIX2": "100",
		}},
		{"singular matrix", map[string]string{
			"CRVAL1": "180", "CRVAL2": "0", "CRPIX1": "100", "CRPIX2": "100",
			"CD1_1": "0.0003", "CD1_2": "0.0003", "CD2_1": "0.0003", "CD2_2": "0.0003",
		}},
		{"non-TAN projection", map[string]string{
			"CTYPE1": "RA---SIN", "CTYPE2": "DEC--SIN",
			"CRVAL1": "180", "CRVAL2": "0", "CRPIX1": "100", "CRPIX2": "100",
			"CDELT1": "0.0003", "CDELT2": "0.0003",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *Header
			if tt.cards != nil {
				h = hdr(tt.cards)
			}
			_, ok := ParseWCS(h)
			assert.False(t, ok)
		})
	}
}

func TestParseWCS_AcceptsTANAndBareHeaders(t *testing.T) {
	w, ok := ParseWCS(hdr(map[string]string{
		"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN",
		"CRVAL1": "210.9", "CRVAL2": "54.3", "CRPIX1": "1860", "CRPIX2": "1395",
		"CDELT1": "-0.000295504", "CDELT2": "0.000295525",
		"PC1_1": "0.0912", "PC1_2": "-0.9958", "PC2_1": "0.9958", "PC2_2": "0.0911",
	}))
	require.True(t, ok)
	assert.InDelta(t, 1.064, w.ScaleArcsecPerPix(), 0.01, "plate scale from CDELT ~0.0002955 deg/px")

	// CTYPE absent (minimal header) is accepted too.
	_, ok = ParseWCS(hdr(map[string]string{
		"CRVAL1": "180", "CRVAL2": "0", "CRPIX1": "100", "CRPIX2": "100",
		"CDELT1": "0.0003", "CDELT2": "0.0003",
	}))
	assert.True(t, ok)
}

func TestWCS_PCCdeltMatchesExplicitCD(t *testing.T) {
	// The same rotated+mirrored solution written both ways must project identically.
	const d1, d2 = -0.000295504, 0.000295525
	const p11, p12, p21, p22 = 0.0912, -0.9958, 0.9958, 0.0911
	base := map[string]string{"CRVAL1": "210.9", "CRVAL2": "54.3", "CRPIX1": "1860", "CRPIX2": "1395"}
	pc := map[string]string{
		"CDELT1": "-0.000295504", "CDELT2": "0.000295525",
		"PC1_1": "0.0912", "PC1_2": "-0.9958", "PC2_1": "0.9958", "PC2_2": "0.0911",
	}
	cd := map[string]string{
		"CD1_1": floatCard(d1 * p11), "CD1_2": floatCard(d1 * p12),
		"CD2_1": floatCard(d2 * p21), "CD2_2": floatCard(d2 * p22),
	}
	for k, v := range base {
		pc[k], cd[k] = v, v
	}
	wPC, ok := ParseWCS(hdr(pc))
	require.True(t, ok)
	wCD, ok := ParseWCS(hdr(cd))
	require.True(t, ok)

	for _, pt := range [][2]float64{{210.9, 54.3}, {211.2, 54.1}, {210.5, 54.62}} {
		x1, y1, ok1 := wPC.SkyToPix(pt[0], pt[1])
		x2, y2, ok2 := wCD.SkyToPix(pt[0], pt[1])
		require.True(t, ok1)
		require.True(t, ok2)
		assert.InDelta(t, x1, x2, 1e-9)
		assert.InDelta(t, y1, y2, 1e-9)
	}
}

func TestWCS_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		cards map[string]string
	}{
		{"plain equatorial field", map[string]string{
			"CRVAL1": "180", "CRVAL2": "0", "CRPIX1": "1000", "CRPIX2": "800",
			"CD1_1": "-0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003",
		}},
		{"rotated 30 degrees", map[string]string{
			"CRVAL1": "83.8", "CRVAL2": "-5.4", "CRPIX1": "2328", "CRPIX2": "1760",
			"CD1_1": "-0.00025981", "CD1_2": "-0.00015", "CD2_1": "-0.00015", "CD2_2": "0.00025981",
		}},
		{"mirrored (positive determinant)", map[string]string{
			"CRVAL1": "12.5", "CRVAL2": "41.2", "CRPIX1": "500", "CRPIX2": "500",
			"CD1_1": "0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003",
		}},
		{"near the celestial pole", map[string]string{
			"CRVAL1": "37.95", "CRVAL2": "89.7", "CRPIX1": "800", "CRPIX2": "600",
			"CD1_1": "-0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003",
		}},
		{"field straddling RA=0", map[string]string{
			"CRVAL1": "359.9", "CRVAL2": "28.1", "CRPIX1": "800", "CRPIX2": "600",
			"CD1_1": "-0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, ok := ParseWCS(hdr(tt.cards))
			require.True(t, ok)
			for _, p := range [][2]float64{{0, 0}, {1234.5, 987.25}, {40, 1500}} {
				ra, dec := w.PixToSky(p[0], p[1])
				assert.GreaterOrEqual(t, ra, 0.0)
				assert.Less(t, ra, 360.0)
				x, y, ok := w.SkyToPix(ra, dec)
				require.True(t, ok)
				assert.InDelta(t, p[0], x, 1e-6, "x round-trip")
				assert.InDelta(t, p[1], y, 1e-6, "y round-trip")
			}
		})
	}
}

func TestWCS_KnownOffsets(t *testing.T) {
	// East-left field at the equator: CD = [[-s,0],[0,s]] → +0.03° of RA is -100 px of x.
	w, ok := ParseWCS(hdr(map[string]string{
		"CRVAL1": "180", "CRVAL2": "0", "CRPIX1": "101", "CRPIX2": "101",
		"CD1_1": "-0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003",
	}))
	require.True(t, ok)

	x, y, ptOK := w.SkyToPix(180, 0)
	require.True(t, ptOK)
	assert.InDelta(t, 100, x, 1e-9, "tangent point lands on CRPIX-1")
	assert.InDelta(t, 100, y, 1e-9)

	x, y, ptOK = w.SkyToPix(180.03, 0)
	require.True(t, ptOK)
	assert.InDelta(t, 0, x, 1e-3, "0.03° East ≈ 100 px left")
	assert.InDelta(t, 100, y, 1e-3)

	x, y, ptOK = w.SkyToPix(180, 0.03)
	require.True(t, ptOK)
	assert.InDelta(t, 100, x, 1e-3)
	assert.InDelta(t, 200, y, 1e-3, "0.03° North ≈ 100 px up axis 2")

	// A point on the far hemisphere never projects.
	_, _, ptOK = w.SkyToPix(0, 0)
	assert.False(t, ptOK)
}

// floatCard formats a float at full precision so the PC↔CD parity test compares the same matrix,
// not a rounded one.
func floatCard(v float64) string {
	return strconv.FormatFloat(v, 'g', 17, 64)
}
