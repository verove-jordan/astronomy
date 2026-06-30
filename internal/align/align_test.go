package align

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// A deterministic rich winter-evening sky over Paris: Orion, Auriga, Taurus, Gemini, Canis Major/Minor
// are all up, so there are plenty of bright stars in any reasonable altitude band.
func planParams() Params {
	return Params{
		At:  time.Date(2026, 1, 15, 21, 0, 0, 0, time.UTC),
		Lat: 48.857,
		Lon: 2.352,
	}
}

func TestPlan_ReturnsRequestedCountInBand(t *testing.T) {
	p := planParams()
	profile := Lookup("eq-generic")
	res := Plan(p, profile, 4, nil, nil)

	require.Len(t, res.Stars, 4, "a rich sky should fill every requested slot")
	for i, s := range res.Stars {
		assert.Equal(t, i+1, s.Order)
		assert.GreaterOrEqual(t, s.AltDeg, profile.MinAltDeg)
		assert.LessOrEqual(t, s.AltDeg, profile.MaxAltDeg)
		assert.NotEmpty(t, s.Reasons)
		assert.NotEmpty(t, s.Compass)
	}
	assert.Equal(t, "recommended", res.Stars[0].Status, "with no accepted stars the first is the one to center now")
	assert.Equal(t, "north", res.Hemisphere)
	assert.Greater(t, res.QualityScore, 0.0)
}

func TestPlan_SpreadBeatsNaiveBrightest(t *testing.T) {
	p := planParams()
	profile := Lookup("eq-generic")
	const n = 5

	res := Plan(p, profile, n, nil, nil)
	require.Len(t, res.Stars, n)

	// Naive baseline: the n brightest eligible stars, ignoring geometry.
	pool := eligible(positionedCatalog(p), profile, "any", nil, nil)
	require.GreaterOrEqual(t, len(pool), n)
	sort.Slice(pool, func(i, j int) bool { return pool[i].Mag < pool[j].Mag })
	brightest := pool[:n]

	assert.GreaterOrEqual(t, meanSepStars(res.Stars), meanSepPositioned(brightest),
		"the spread-aware plan should be at least as well spread as the brightest-n set")
	assert.Greater(t, meanSepStars(res.Stars), 25.0, "chosen stars should be genuinely spread out")
}

func TestPlan_SkipExcludesAndReplaces(t *testing.T) {
	p := planParams()
	profile := Lookup("eq-generic")

	first := Plan(p, profile, 3, nil, nil)
	require.Len(t, first.Stars, 3)
	skipped := first.Stars[0].Name

	after := Plan(p, profile, 3, nil, []string{skipped})
	require.Len(t, after.Stars, 3, "skipping must pull in a replacement, not shrink the plan")
	for _, s := range after.Stars {
		assert.NotEqual(t, skipped, s.Name, "the skipped star must not reappear")
	}
}

func TestPlan_AcceptedStarLockedFirst(t *testing.T) {
	p := planParams()
	profile := Lookup("eq-generic")

	base := Plan(p, profile, 3, nil, nil)
	require.Len(t, base.Stars, 3)
	keep := base.Stars[2].Name // a real, currently-visible star

	res := Plan(p, profile, 3, []string{keep}, nil)
	require.Len(t, res.Stars, 3)
	assert.Equal(t, keep, res.Stars[0].Name)
	assert.Equal(t, "accepted", res.Stars[0].Status)
	assert.Equal(t, "recommended", res.Stars[1].Status)
}

func TestPlan_SameMeridianSideStaysOnOneSide(t *testing.T) {
	p := planParams()
	res := Plan(p, Lookup("synscan-eq"), 3, nil, nil)
	require.NotEmpty(t, res.Stars)
	assert.Contains(t, []string{"east", "west"}, res.MeridianSide)
	for _, s := range res.Stars {
		assert.Equal(t, res.MeridianSide, s.MeridianSide, "SynScan keeps every align star on one side")
	}
}

func TestPlan_AltAzAvoidsZenith(t *testing.T) {
	p := planParams()
	profile := Lookup("altaz-generic")
	res := Plan(p, profile, 4, nil, nil)
	require.NotEmpty(t, res.Stars)
	for _, s := range res.Stars {
		assert.LessOrEqual(t, s.AltDeg, profile.MaxAltDeg, "alt-az must keep stars below the zenith band")
		assert.GreaterOrEqual(t, s.AltDeg, profile.MinAltDeg)
	}
}

func TestPlan_NoCandidatesWarnsInsteadOfPanicking(t *testing.T) {
	p := planParams()
	var all []string
	for _, s := range Catalog() {
		all = append(all, s.Name)
	}
	res := Plan(p, Lookup("eq-generic"), 3, nil, all) // reject the entire sky
	assert.Empty(t, res.Stars)
	assert.NotEmpty(t, res.Warnings)
}

func meanSepStars(stars []AlignStar) float64 {
	var sum, n float64
	for i := 0; i < len(stars); i++ {
		for j := i + 1; j < len(stars); j++ {
			sum += astro.AngularSeparation(stars[i].RADeg, stars[i].DecDeg, stars[j].RADeg, stars[j].DecDeg)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func meanSepPositioned(stars []positioned) float64 {
	var sum, n float64
	for i := 0; i < len(stars); i++ {
		for j := i + 1; j < len(stars); j++ {
			sum += astro.AngularSeparation(stars[i].raNow, stars[i].decNow, stars[j].raNow, stars[j].decNow)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}
