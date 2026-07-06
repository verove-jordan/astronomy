package calib

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSized(t *testing.T, dir, name string, n int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPoolSignature(t *testing.T) {
	dir := t.TempDir()
	a := writeSized(t, dir, "a.fits", 100)
	b := writeSized(t, dir, "b.fits", 200)

	sig := poolSignature([]string{a, b})
	if sig == "" {
		t.Fatal("empty signature")
	}
	// Order-independent: the same pool in any order yields the same signature.
	if got := poolSignature([]string{b, a}); got != sig {
		t.Fatalf("signature is order-dependent: %s vs %s", sig, got)
	}
	// A changed frame (size or mtime) changes the signature → a rebuild.
	writeSized(t, dir, "b.fits", 999)
	if got := poolSignature([]string{a, b}); got == sig {
		t.Fatal("signature unchanged after a frame was rewritten (larger)")
	}
	// A newer mtime alone changes it.
	sig2 := poolSignature([]string{a, b})
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(b, future, future); err != nil {
		t.Fatal(err)
	}
	if got := poolSignature([]string{a, b}); got == sig2 {
		t.Fatal("signature unchanged after mtime bump")
	}
	// A different pool membership changes it.
	if got := poolSignature([]string{a}); got == poolSignature([]string{a, b}) {
		t.Fatal("dropping a frame did not change the signature")
	}
}
