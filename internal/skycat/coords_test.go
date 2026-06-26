package skycat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseRA(t *testing.T) {
	tests := []struct {
		name, in string
		want     float64
		ok       bool
	}{
		{"sexagesimal hours space", "13 29 52.7", 202.469583, true},
		{"sexagesimal hours colon", "13:29:52.7", 202.469583, true},
		{"decimal degrees", "202.47", 202.47, true},
		{"zero hours", "00 00 00", 0, true},
		{"empty", "", 0, false},
		{"garbage", "abc", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseRA(tt.in)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.InDelta(t, tt.want, got, 0.001)
			}
		})
	}
}

func TestParseDec(t *testing.T) {
	tests := []struct {
		name, in string
		want     float64
		ok       bool
	}{
		{"positive sexagesimal", "+47 11 43", 47.195278, true},
		{"negative sexagesimal", "-05 23 00", -5.383333, true},
		{"negative colon", "-05:23:00", -5.383333, true},
		{"decimal degrees", "47.19", 47.19, true},
		{"empty", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseDec(tt.in)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.InDelta(t, tt.want, got, 0.001)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, "M101", Normalize("m 101"))
	assert.Equal(t, "NGC5457", Normalize("ngc-5457"))
	assert.Equal(t, "", Normalize("  "))
}
