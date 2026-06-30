package align

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookup_DefaultsOnUnknown(t *testing.T) {
	assert.Equal(t, "eq-generic", Lookup("").Key)
	assert.Equal(t, "eq-generic", Lookup("does-not-exist").Key)
	assert.Equal(t, "synscan-eq", Lookup("synscan-eq").Key)
	assert.True(t, Lookup("celestron-eq").SameMeridianSide)
	assert.Equal(t, "altaz", Lookup("altaz-generic").MountType)
}

func TestProfile_ClampCount(t *testing.T) {
	p := Lookup("eq-generic") // Default 3, Min 2, Max 6
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"non-positive falls back to default", 0, 3},
		{"negative falls back to default", -4, 3},
		{"below min clamps up", 1, 2},
		{"in range unchanged", 4, 4},
		{"above max clamps down", 99, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p.ClampCount(tt.in))
		})
	}
}

func TestProfiles_RegistryIsSane(t *testing.T) {
	for _, p := range Profiles() {
		t.Run(p.Key, func(t *testing.T) {
			assert.NotEmpty(t, p.Label)
			assert.Contains(t, []string{"eq", "altaz"}, p.MountType)
			assert.Less(t, p.MinAltDeg, p.MaxAltDeg)
			assert.LessOrEqual(t, p.MinStars, p.DefaultStars)
			assert.LessOrEqual(t, p.DefaultStars, p.MaxStars)
			assert.Positive(t, p.MagLimit)
			assert.NotEmpty(t, p.Note)
		})
	}
}
