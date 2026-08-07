package astro

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Provence — a mid-northern site where the Moon rises and sets every day.
const (
	testLat = 43.6
	testLon = 5.1
)

// The factor is the whole reason moonlit hours count for less, so its relationship to the Moon's own
// altitude has to hold exactly, not approximately: a factor below 1 while the Moon is down would
// penalise a night for nothing.
func TestMoonGlowFactor_UnityExactlyWhileTheMoonIsDown(t *testing.T) {
	start := time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 48; i++ { // a full day at 30-minute steps covers every rise and set
		at := start.Add(time.Duration(i) * 30 * time.Minute)
		up := moonAltitude(at, testLat, testLon) > moonHorizonDeg
		factor := MoonGlowFactor(at, testLat, testLon)

		if up {
			assert.LessOrEqual(t, factor, 1.0, "at %s", at)
		} else {
			assert.Equal(t, 1.0, factor, "Moon below the horizon at %s must cost nothing", at)
		}
		assert.GreaterOrEqual(t, factor, 1-moonGlowStrength, "at %s", at)
	}
}

func TestMoonGlowFactor_FullMoonCostsMoreThanNew(t *testing.T) {
	// August 2026: new Moon around the 12th, full Moon around the 28th. Sample each at local midnight,
	// when the full Moon is near culmination and the new Moon is with the Sun.
	newMoon := time.Date(2026, time.August, 12, 23, 0, 0, 0, time.UTC)
	fullMoon := time.Date(2026, time.August, 28, 23, 0, 0, 0, time.UTC)

	atNew := MoonGlowFactor(newMoon, testLat, testLon)
	atFull := MoonGlowFactor(fullMoon, testLat, testLon)

	assert.Less(t, atFull, atNew, "a full Moon overhead must spoil the night more than a new one")
	assert.Less(t, atFull, 0.6)
}

func TestMoonGlowFactor_SinksAsTheMoonRises(t *testing.T) {
	// Around a full Moon the illumination barely moves, so the factor must track altitude alone.
	base := time.Date(2026, time.August, 28, 18, 0, 0, 0, time.UTC)

	var prevAlt, prevFactor float64
	rising := 0
	for i := 0; i < 12; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		alt := moonAltitude(at, testLat, testLon)
		factor := MoonGlowFactor(at, testLat, testLon)
		if i > 0 && alt > prevAlt && alt > 5 {
			assert.LessOrEqual(t, factor, prevFactor, "a higher Moon cannot be kinder, at %s", at)
			rising++
		}
		prevAlt, prevFactor = alt, factor
	}
	assert.Greater(t, rising, 0, "the sampled span must actually contain the Moon climbing")
}

func TestMoonUpHours_Bounds(t *testing.T) {
	tests := []struct {
		name string
		date time.Time
	}{
		{name: "new moon", date: time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)},
		{name: "first quarter", date: time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)},
		{name: "full moon", date: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)},
		{name: "midwinter", date: time.Date(2026, time.December, 21, 12, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NightWindow(tt.date, testLat, testLon, -18)

			up := MoonUpHours(w, testLat, testLon)

			assert.GreaterOrEqual(t, up, 0.0)
			assert.LessOrEqual(t, up, w.Hours()+1e-6, "the Moon cannot be up longer than the night")
		})
	}
}

// Over a whole day the Moon is above the horizon for roughly half the time at mid latitudes; this
// pins the crossing walk against an off-by-one that would report 0 or the full span.
func TestMoonUpHours_AboutHalfOfAFullDay(t *testing.T) {
	start := time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC)
	day := DarkWindow{Start: start, End: start.Add(24 * time.Hour)}

	up := MoonUpHours(day, testLat, testLon)

	assert.InDelta(t, 12.0, up, 3.0)
}

func TestMoonUpHours_EmptyWindowIsZero(t *testing.T) {
	at := time.Date(2026, time.August, 4, 22, 0, 0, 0, time.UTC)

	assert.Zero(t, MoonUpHours(DarkWindow{Start: at, End: at}, testLat, testLon))
	assert.Zero(t, MoonUpHours(DarkWindow{Start: at, End: at.Add(-time.Hour)}, testLat, testLon))
}
