package skycat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCatalog(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	writeCatalog(t, dir, "messier.csv",
		"name,ra,dec,diameter,mag,alias\nM101,210.80208,54.348917,26.9,7.7,Pinwheel galaxy/NGC5457\n")
	writeCatalog(t, dir, "ngc.csv", "name,ra,dec,diameter,mag,alias\nNGC1,1.816245,27.708889,1.7,12.9,\n")

	tests := []struct {
		name, query, want string
		ok                bool
	}{
		{"primary name", "M101", "210.80208,54.348917", true},
		{"spaces + case", "m 101", "210.80208,54.348917", true},
		{"alias NGC", "NGC5457", "210.80208,54.348917", true},
		{"alias words", "Pinwheel Galaxy", "210.80208,54.348917", true},
		{"other catalog", "NGC1", "1.816245,27.708889", true},
		{"unknown", "M999", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Resolve(tt.query, dir)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolve_NoDir(t *testing.T) {
	_, ok := Resolve("M101", "")
	assert.False(t, ok)
}
