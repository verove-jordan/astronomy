package api

import (
	"path/filepath"
	"testing"
)

// relForData is the crux of the "Réutiliser" fix: the folder rel returned by /api/processed must be the
// DataDir-relative slash path (the ledger key the S3 pull uses), and must refuse a path outside DataDir.
func TestRelForData(t *testing.T) {
	dataAbs, _ := filepath.Abs("/data")
	cases := []struct {
		name string
		abs  string
		want string
	}{
		{"nested capture folder", "/data/M92/darks", "M92/darks"},
		{"top-level object folder", "/data/M101", "M101"},
		{"deep nested", "/data/M92/session1/flats", "M92/session1/flats"},
		{"outside DataDir", "/other/x", ""},
		{"sibling prefix is not under DataDir", "/data-else/x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := relForData(dataAbs, c.abs); got != c.want {
				t.Fatalf("relForData(%q, %q) = %q, want %q", dataAbs, c.abs, got, c.want)
			}
		})
	}
}
