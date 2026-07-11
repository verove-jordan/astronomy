package localfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDrive builds a temp "mount root" with two drives and a stray file, returning the root (as an
// allowlist would hold it) and its symlink-resolved form (what Allowed/Browse return):
//
//	root/
//	  DriveA/photos/a.fits (10B), DriveA/notes.txt (5B), DriveA/.hidden/
//	  DriveB/
//	  loose.txt
//	  .DS_Store
func fakeDrive(t *testing.T) (root, rootReal string) {
	t.Helper()
	root = t.TempDir()
	mkdir := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		require.NoError(t, os.MkdirAll(p, 0o755))
		return p
	}
	write := func(data string, parts ...string) {
		p := filepath.Join(append([]string{root}, parts...)...)
		require.NoError(t, os.WriteFile(p, []byte(data), 0o644))
	}
	mkdir("DriveA", "photos")
	mkdir("DriveA", ".hidden")
	mkdir("DriveB")
	write("aaaaaaaaaa", "DriveA", "photos", "a.fits")
	write("notes", "DriveA", "notes.txt")
	write("x", "loose.txt")
	write("y", ".DS_Store")

	real, err := filepath.EvalSymlinks(root) // macOS temp dirs live under /var → /private/var
	require.NoError(t, err)
	return root, real
}

func TestAllowed(t *testing.T) {
	root, rootReal := fakeDrive(t)
	roots := []string{root}

	tests := []struct {
		name   string
		path   string
		wantOK bool
	}{
		{"root itself", root, true},
		{"a drive under the root", filepath.Join(root, "DriveA"), true},
		{"a nested folder", filepath.Join(root, "DriveA", "photos"), true},
		{"blank path", "", false},
		{"nonexistent path", filepath.Join(root, "nope"), false},
		{"traversal escapes the root", filepath.Join(root, "DriveA", "..", "..", "etc"), false},
		{"absolute outside path", "/etc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Allowed(roots, tt.path)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				// The resolved path is returned and lives within the resolved root.
				assert.True(t, got == rootReal || strings.HasPrefix(got, rootReal+string(os.PathSeparator)),
					"got %q under %q", got, rootReal)
			}
		})
	}
}

func TestAllowed_RejectsSymlinkEscape(t *testing.T) {
	root, _ := fakeDrive(t)
	outside := t.TempDir() // a sibling temp dir, NOT under root
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644))
	link := filepath.Join(root, "DriveA", "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	// The symlink itself and any path through it must be rejected (resolves outside the allowlist).
	_, ok := Allowed([]string{root}, link)
	assert.False(t, ok, "a symlink pointing outside the root must be rejected")
	_, ok = Allowed([]string{root}, filepath.Join(link, "secret.txt"))
	assert.False(t, ok, "a path THROUGH an escaping symlink must be rejected")

	_, err := Browse([]string{root}, link)
	assert.ErrorIs(t, err, ErrForbidden)
}

func TestBrowse(t *testing.T) {
	root, rootReal := fakeDrive(t)
	roots := []string{root}

	t.Run("lists folders first then files, hides dotfiles, sizes files", func(t *testing.T) {
		got, err := Browse(roots, filepath.Join(root, "DriveA"))
		require.NoError(t, err)
		names := make([]string, len(got.Entries))
		for i, e := range got.Entries {
			names[i] = e.Name
		}
		assert.Equal(t, []string{"photos", "notes.txt"}, names, "dirs before files, .hidden dropped")
		assert.True(t, got.Entries[0].IsDir)
		assert.False(t, got.Entries[1].IsDir)
		assert.Equal(t, int64(5), got.Entries[1].Size)
		assert.Equal(t, rootReal, got.Parent, "parent is the (resolved) root, which is allowed")
	})

	t.Run("no parent above a root", func(t *testing.T) {
		got, err := Browse(roots, root)
		require.NoError(t, err)
		assert.Empty(t, got.Parent, "up-navigation stops at the allowlist root")
	})

	t.Run("forbidden outside the allowlist", func(t *testing.T) {
		_, err := Browse(roots, filepath.Dir(rootReal))
		assert.ErrorIs(t, err, ErrForbidden)
	})

	t.Run("empty folder yields a non-nil entries slice", func(t *testing.T) {
		got, err := Browse(roots, filepath.Join(root, "DriveB"))
		require.NoError(t, err)
		assert.NotNil(t, got.Entries)
		assert.Empty(t, got.Entries)
	})
}

func TestDrives(t *testing.T) {
	root, _ := fakeDrive(t)
	drives := Drives([]string{root})

	names := make([]string, len(drives))
	for i, d := range drives {
		names[i] = d.Name
	}
	assert.Equal(t, []string{"DriveA", "DriveB"}, names, "immediate sub-dirs only, name-sorted, files/dotfiles skipped")
	for _, d := range drives {
		assert.True(t, filepath.IsAbs(d.Path))
	}
}

// A drive entry that is a symlink escaping the allowlist (like macOS's "Macintosh HD" -> /) is dropped, so
// every listed drive is actually browsable.
func TestDrives_ExcludesSymlinkEscape(t *testing.T) {
	root, _ := fakeDrive(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Escape")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	names := map[string]bool{}
	for _, d := range Drives([]string{root}) {
		names[d.Name] = true
	}
	assert.True(t, names["DriveA"], "real drive DriveA is listed")
	assert.True(t, names["DriveB"], "real drive DriveB is listed")
	assert.False(t, names["Escape"], "a drive symlinked outside the allowlist is dropped")
}

func TestRoots_KeepsOnlyExisting(t *testing.T) {
	existing := t.TempDir()
	roots := Roots([]string{existing, filepath.Join(existing, "does-not-exist"), ""})
	assert.Contains(t, roots, filepath.Clean(existing))
	assert.NotContains(t, roots, filepath.Join(existing, "does-not-exist"))
}
