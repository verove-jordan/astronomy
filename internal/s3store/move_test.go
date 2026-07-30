package s3store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// MoveDest is the shared key math for the explorer move (handler guards + move job). It must place src's base
// name under the destination folder, keep the trailing "/" for a folder, and let callers detect the no-op
// (newKey == src) and folder-into-itself (HasPrefix(newKey, src)) cases.
func TestMoveDest(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		dst     string
		wantKey string
		wantDir bool
	}{
		{"file into folder", "a/b.fit", "x", "x/b.fit", false},
		{"file into nested folder", "a/b.fit", "x/y", "x/y/b.fit", false},
		{"file into root", "a/b.fit", "", "b.fit", false},
		{"file, dst already slashed", "a/b.fit", "x/", "x/b.fit", false},
		{"folder into folder", "a/", "x", "x/a/", true},
		{"folder into root (no-op)", "a/", "", "a/", true},
		{"folder into deeper self", "a/", "a/b", "a/b/a/", true},
		{"root object into folder", "b.fit", "x", "x/b.fit", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, isDir := MoveDest(c.src, c.dst)
			assert.Equal(t, c.wantKey, key)
			assert.Equal(t, c.wantDir, isDir)
		})
	}

	// No-op: a folder moved into its own place resolves to itself.
	key, _ := MoveDest("a/", "")
	assert.Equal(t, "a/", key, "folder into root is a no-op (newKey == src)")

	// Folder-into-itself: moving a/ under a/b yields a key that starts with src (caller rejects it).
	inside, isDir := MoveDest("a/", "a/b")
	assert.True(t, isDir)
	assert.True(t, len(inside) >= len("a/") && inside[:len("a/")] == "a/", "into-itself starts with src")
}
