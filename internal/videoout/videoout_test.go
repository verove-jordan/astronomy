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
