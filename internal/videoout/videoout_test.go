package videoout

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestArgs(t *testing.T) {
	a := Args("/o/final.png", "/o/final.mp4", Options{Seconds: 10, Width: 1280, Height: 720})
	joined := strings.Join(a, " ")

	assert.Contains(t, joined, "-loop 1")
	assert.Contains(t, joined, "-i /o/final.png")
	assert.Contains(t, joined, "-t 10")
	assert.Contains(t, joined, "zoompan")
	assert.Contains(t, joined, "s=1280x720")
	assert.Contains(t, joined, "libx264")
	assert.Equal(t, "/o/final.mp4", a[len(a)-1])
}

func TestArgs_DefaultsOnZero(t *testing.T) {
	a := Args("in.png", "out.mp4", Options{})
	assert.Contains(t, strings.Join(a, " "), "-t 12") // DefaultOptions seconds
}

// TestOptionsFor pins the aspect-preserving sizing: same pixel budget as 720p, even dims, no
// stretching of 4:3 sensors or mosaic canvases into 16:9.
func TestOptionsFor(t *testing.T) {
	tests := []struct {
		name  string
		w, h  int
		wantW int
		wantH int
	}{
		{"16:9 keeps the classic 720p", 1920, 1080, 1280, 720},
		{"4:3 sensor", 4656, 3520, 1104, 834},
		{"square", 1000, 1000, 960, 960},
		{"invalid falls back", 0, 100, 1280, 720},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := OptionsFor(tt.w, tt.h)
			if o.Width%2 != 0 || o.Height%2 != 0 {
				t.Fatalf("odd dims %dx%d", o.Width, o.Height)
			}
			if o.Width != tt.wantW || o.Height != tt.wantH {
				t.Fatalf("got %dx%d want %dx%d", o.Width, o.Height, tt.wantW, tt.wantH)
			}
			if o.Seconds != 12 {
				t.Fatalf("seconds %d", o.Seconds)
			}
		})
	}
}
