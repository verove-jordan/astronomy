package fits

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// hdr builds a Header from raw card values for testing (same-package access to the unexported fields).
func hdr(cards map[string]string) *Header {
	h := &Header{index: map[string]int{}}
	for k, v := range cards {
		h.index[k] = len(h.cards)
		h.cards = append(h.cards, Card{Key: k, Value: v})
	}
	return h
}

func TestHeader_CDDeterminant(t *testing.T) {
	tests := []struct {
		name     string
		cards    map[string]string
		wantOK   bool
		wantSign int // -1, +1, or 0 when !ok
	}{
		{
			// Real Siril 1.4.3 solve of the primary rig: PC ~ proper rotation (det>0), CDELT1<0 → det(CD)<0.
			name: "siril PC+CDELT, primary rig East-left",
			cards: map[string]string{
				"CDELT1": "-0.000295504", "CDELT2": "0.000295525",
				"PC1_1": "0.0912", "PC1_2": "-0.9958", "PC2_1": "0.9958", "PC2_2": "0.0911",
			},
			wantOK: true, wantSign: -1,
		},
		{
			// The case CDELT-alone misses: both CDELT positive (product > 0), but a reflection is folded
			// into PC (det(PC) = -1), so the true det(CD) is NEGATIVE. CDELT-alone would read the sign wrong.
			name: "reflection hidden in PC matrix",
			cards: map[string]string{
				"CDELT1": "0.0003", "CDELT2": "0.0003",
				"PC1_1": "1", "PC1_2": "0", "PC2_1": "0", "PC2_2": "-1",
			},
			wantOK: true, wantSign: -1,
		},
		{
			// Mirrored frame: CDELT1 positive (East-right) with identity PC → det(CD) > 0 → needs a flip.
			name:   "mirrored frame, positive CDELT1, PC identity",
			cards:  map[string]string{"CDELT1": "0.0003", "CDELT2": "0.0003"},
			wantOK: true, wantSign: 1,
		},
		{
			name:   "explicit CD matrix, East-left",
			cards:  map[string]string{"CD1_1": "-0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003"},
			wantOK: true, wantSign: -1,
		},
		{
			name:   "explicit CD matrix takes precedence over CDELT",
			cards:  map[string]string{"CD1_1": "0.0003", "CD1_2": "0", "CD2_1": "0", "CD2_2": "0.0003", "CDELT1": "-1", "CDELT2": "1"},
			wantOK: true, wantSign: 1,
		},
		{name: "no WCS at all", cards: map[string]string{"GAIN": "300"}, wantOK: false},
		{
			name:   "degenerate zero determinant",
			cards:  map[string]string{"CDELT1": "0.0003", "CDELT2": "0", "PC1_1": "1", "PC2_2": "1"},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			det, ok := hdr(tt.cards).CDDeterminant()
			assert.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			if tt.wantSign < 0 {
				assert.Negative(t, det)
			} else {
				assert.Positive(t, det)
			}
		})
	}
}
