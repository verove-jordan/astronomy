package solarsystem

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Surface textures.
//
// The maps are downloaded rather than vendored — they are tens of megabytes and carry their own
// licences, so they live beside the light-pollution and canopy atlases under the work directory and
// are fetched by a just recipe. Everything here treats them as optional: an engine with none of them
// serves an empty list and every body falls back to procedural shading.

// TextureDirName is the folder under the work directory the download recipe fills.
const TextureDirName = "solarsystem"

// textureExts are the formats accepted, best first — a key present in more than one wins with the
// smallest to download.
var textureExts = []string{".webp", ".jpg", ".jpeg", ".png"}

// textureKey is the whole of the path confinement: a key is a bare lowercase word, so no key can
// ever name a file outside the texture directory. Validating the shape beats sanitising the path.
var textureKey = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// TextureDir returns the directory textures live in, given the engine's work directory.
func TextureDir(workDir string) string {
	return filepath.Join(workDir, TextureDirName)
}

// Textures lists the texture keys present in dir, sorted. A missing or unreadable directory is not
// an error — it is simply an engine nobody has run the download recipe on.
func Textures(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !accepted(ext) {
			continue
		}
		key := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		if textureKey.MatchString(key) {
			seen[key] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TexturePath resolves a texture key to a readable file inside dir. It reports false for an unknown
// key, an absent file, and anything whose name is not a bare word.
func TexturePath(dir, key string) (string, bool) {
	if !textureKey.MatchString(key) {
		return "", false
	}
	for _, ext := range textureExts {
		p := filepath.Join(dir, key+ext)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

func accepted(ext string) bool {
	for _, e := range textureExts {
		if e == ext {
			return true
		}
	}
	return false
}
