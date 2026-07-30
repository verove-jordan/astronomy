package calib

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func TestPoolSigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.sig")

	if h, c := readPoolSig(p); h != "" || c != 0 {
		t.Fatalf("missing sidecar → (%q,%d)", h, c)
	}
	if err := os.WriteFile(p, []byte("abc123"), 0o644); err != nil { // v1: hash only
		t.Fatal(err)
	}
	if h, c := readPoolSig(p); h != "abc123" || c != 0 {
		t.Fatalf("v1 sidecar → (%q,%d)", h, c)
	}
	if err := os.WriteFile(p, []byte(formatPoolSig("abc123", 42)), 0o644); err != nil {
		t.Fatal(err)
	}
	if h, c := readPoolSig(p); h != "abc123" || c != 42 {
		t.Fatalf("v2 sidecar → (%q,%d)", h, c)
	}
}

// TestStackPooled_ShrinkGuard: a pool that lost frames (freed to S3) must NEVER rebuild the master
// shallower — the existing deep master is kept byte-untouched, with its recorded depth and a note.
func TestStackPooled_ShrinkGuard(t *testing.T) {
	lib := t.TempDir()
	key := inspect.SetKey{Type: inspect.Dark, Gain: 139, Offset: 21, Bin: 1, ExposureMs: 120000, TempBucket: -15}
	name := masterName(MasterDark, key)
	masterPath := filepath.Join(lib, name+".fits")
	if err := os.WriteFile(masterPath, []byte("DEEP MASTER BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, name+".sig"), []byte(formatPoolSig("old-pool-hash", 5)), 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range []string{"d1.fits", "d2.fits"} { // 2 < 5: the pool shrank
		p := filepath.Join(t.TempDir(), n)
		if err := os.WriteFile(p, []byte("raw"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	m, note, err := stackPooled(context.Background(), nil, MasterDark, key, paths, lib, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.FrameCount != 5 {
		t.Fatalf("kept master must report its recorded depth, got %d", m.FrameCount)
	}
	if !strings.Contains(note, "kept the existing deeper master") {
		t.Fatalf("want a shrink note, got %q", note)
	}
	b, _ := os.ReadFile(masterPath)
	if string(b) != "DEEP MASTER BYTES" {
		t.Fatal("the existing master file must stay byte-untouched")
	}
}

// TestStackPooled_SigUpgrade: an unchanged pool with a v1 sidecar reuses the master AND upgrades the
// sidecar in place to record the depth the shrink guard needs.
func TestStackPooled_SigUpgrade(t *testing.T) {
	lib := t.TempDir()
	key := inspect.SetKey{Type: inspect.Bias, Gain: 139, Offset: 21, Bin: 1}
	name := masterName(MasterBias, key)
	if err := os.WriteFile(filepath.Join(lib, name+".fits"), []byte("M"), 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range []string{"b1.fits", "b2.fits", "b3.fits"} {
		p := filepath.Join(lib, n)
		if err := os.WriteFile(p, []byte("raw"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if err := os.WriteFile(filepath.Join(lib, name+".sig"), []byte(poolSignature(paths)), 0o644); err != nil { // v1
		t.Fatal(err)
	}

	m, note, err := stackPooled(context.Background(), nil, MasterBias, key, paths, lib, t.TempDir(), nil)
	if err != nil || note != "" {
		t.Fatalf("unchanged pool must reuse silently, got note=%q err=%v", note, err)
	}
	if m.FrameCount != 3 {
		t.Fatalf("FrameCount = %d, want 3", m.FrameCount)
	}
	if _, c := readPoolSig(filepath.Join(lib, name + ".sig")); c != 3 {
		t.Fatalf("v1 sidecar must upgrade to v2 with the pool depth, got count %d", c)
	}
}

// TestLibraryFallbacks pins the raws-freed substitutes: matching camera signature (+ exposure and
// temperature tolerance for darks), on-disk file required, deepest (nearest-temperature) wins.
func TestLibraryFallbacks(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("m"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rows := []Master{
		{Type: MasterBias, Gain: 139, Offset: 21, Bin: 1, FrameCount: 40, Path: mk("bias40.fits")},
		{Type: MasterBias, Gain: 139, Offset: 21, Bin: 1, FrameCount: 90, Path: mk("bias90.fits")},
		{Type: MasterBias, Gain: 200, Offset: 21, Bin: 1, FrameCount: 500, Path: mk("biasG200.fits")},
		{Type: MasterBias, Gain: 139, Offset: 21, Bin: 1, FrameCount: 999, Path: filepath.Join(dir, "gone.fits")},
		{Type: MasterDark, Gain: 139, Offset: 21, Bin: 1, ExposureMs: 120000, TempMilliC: -15000, FrameCount: 30, Path: mk("d15.fits")},
		{Type: MasterDark, Gain: 139, Offset: 21, Bin: 1, ExposureMs: 120000, TempMilliC: -12000, FrameCount: 80, Path: mk("d12.fits")},
		{Type: MasterDark, Gain: 139, Offset: 21, Bin: 1, ExposureMs: 60000, TempMilliC: -15000, FrameCount: 99, Path: mk("d60s.fits")},
	}

	b := libraryBiasFallback(rows, cameraSig{139, 21, 1})
	if b == nil || b.FrameCount != 90 {
		t.Fatalf("bias fallback: want the deepest on-disk gain-139 master (90), got %+v", b)
	}
	if libraryBiasFallback(rows, cameraSig{300, 0, 1}) != nil {
		t.Fatal("bias fallback must not cross camera signatures")
	}

	d := libraryDarkFallback(rows, darkSig{cameraSig{139, 21, 1}, 120000, -15}, 5)
	if d == nil || d.TempMilliC != -15000 {
		t.Fatalf("dark fallback: nearest temperature must win, got %+v", d)
	}
	if libraryDarkFallback(rows, darkSig{cameraSig{139, 21, 1}, 120000, -30}, 5) != nil {
		t.Fatal("dark fallback must respect the temperature tolerance")
	}
	if libraryDarkFallback(rows, darkSig{cameraSig{139, 21, 1}, 30000, -15}, 5) != nil {
		t.Fatal("dark fallback must require the same exposure")
	}
}
