package inspect

import (
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
