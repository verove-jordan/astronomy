package inspect

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind classifies what a process path points at.
type Kind string

const (
	KindFITS  Kind = "fits"  // directory of monochrome FITS frames (deepsky/nebula)
	KindRaw   Kind = "raw"   // directory of one-shot-color stills (iPhone DNG/HEIC, jpg/png/tif)
	KindVideo Kind = "video" // a single video file (planetary/lunar)
)

// rawExts are one-shot-color still formats Siril can ingest (RAW via libraw, plus HEIF/jpg/png/tif).
var rawExts = map[string]bool{
	".dng": true, ".heic": true, ".heif": true,
	".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true,
	".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true,
}

// DetectInput inspects a path and reports whether it's a video file, a FITS-frame directory, or a
// directory of one-shot-color stills.
func DetectInput(path string) (Kind, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		if videoExts[strings.ToLower(filepath.Ext(path))] {
			return KindVideo, nil
		}
		return "", fmt.Errorf("unsupported file %q (expected a video, or a directory of frames)", path)
	}

	var fitsN, rawN int
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch ext := strings.ToLower(filepath.Ext(p)); {
		case fitsExts[ext]:
			fitsN++
		case rawExts[ext]:
			rawN++
		}
		return nil
	})
	switch {
	case fitsN > 0:
		return KindFITS, nil
	case rawN > 0:
		return KindRaw, nil
	default:
		return "", fmt.Errorf("no FITS or raw image files found in %s", path)
	}
}

// ListFITSFrames returns the FITS frame files under dir, sorted by name (acquisition order).
func ListFITSFrames(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fitsExts[strings.ToLower(filepath.Ext(p))] {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// IsOSCDir reports whether dir holds GENUINE one-shot-color (Bayer CFA) FITS frames that must be
// debayered — i.e. it should run through the OSC pipeline, not the mono per-filter pipeline. It scans
// the directory and applies the same spurious-BAYERPAT veto as the inventory (clearSpuriousBayer), so a
// MONO filter-wheel session whose older-ASICAP frames carry a stray BAYERPAT — which a raw header check
// would misread as colour — is correctly reported as NOT OSC. True only when frames remain CFA after the
// veto (no filter-wheel / mono-filter evidence anywhere), i.e. a real OSC capture. A mono or mixed
// directory is never misrouted. Channel detection is off: the veto only needs header/wheel/manifest
// filters, which resolve without it.
func IsOSCDir(dir string) bool {
	inv, err := ScanWithOptions(context.Background(), dir, ScanOptions{})
	if err != nil || len(inv.Frames) == 0 {
		return false
	}
	for _, fr := range inv.Frames {
		if fr.Bayer == "" {
			return false // a monochrome (or spurious-Bayer-cleared) frame → not an OSC directory
		}
	}
	return true
}

// ListRawFrames returns the one-shot-color still files under dir, sorted (acquisition order).
func ListRawFrames(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rawExts[strings.ToLower(filepath.Ext(p))] {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// ListFITSFramesMany returns the FITS frames under all dirs, concatenated then sorted by full path
// (which groups each directory's frames together, in acquisition order) — the OSC multi-folder scan.
func ListFITSFramesMany(dirs []string) ([]string, error) {
	return listFramesMany(dirs, ListFITSFrames)
}

// ListRawFramesMany returns the one-shot-color stills under all dirs, concatenated then sorted.
func ListRawFramesMany(dirs []string) ([]string, error) {
	return listFramesMany(dirs, ListRawFrames)
}

func listFramesMany(dirs []string, list func(string) ([]string, error)) ([]string, error) {
	var out []string
	for _, d := range dirs {
		frames, err := list(d)
		if err != nil {
			return nil, err
		}
		out = append(out, frames...)
	}
	sort.Strings(out)
	return out, nil
}
