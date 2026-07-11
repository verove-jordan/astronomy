// Package s3layout classifies a capture folder's files into the S3 bucket layout the UI expects at the
// bucket root: darks/, offsets/ (bias), flats/, and lum/<object>/<date>/ for the lights. It is a PURE
// key builder — no filesystem or S3 I/O — so the detector is unit-testable. Callers (internal/job) build
// the []FileInfo from an inspection + a filesystem walk, and persist the resulting rel→key map so the
// classified (non-reversible) keys can be recovered later; anything unclassifiable falls back to the
// legacy data/<rel> mirror so an upload is never blocked.
package s3layout

import (
	"fmt"
	"path"
)

// FrameType is the coarse class of a file, mapped by the caller from an inspected frame. A darkflat maps
// to Flat (it calibrates flats). Unknown files inherit their directory's majority type, else fall back.
type FrameType string

const (
	Light   FrameType = "light"
	Dark    FrameType = "dark"
	Flat    FrameType = "flat"
	Bias    FrameType = "bias"
	Unknown FrameType = ""
)

// FileInfo is one file to place, built by the caller from an inspected frame + a filesystem walk.
type FileInfo struct {
	Rel       string // slash path relative to the source folder root, e.g. "L/light_0001.fits"
	Type      FrameType
	DateObsMs int64  // FITS DATE-OBS epoch ms (0 if unknown)
	MTimeMs   int64  // file mtime epoch ms (last-resort date)
	Object    string // FITS OBJECT header (fallback object detection)
}

// Plan maps each file's Rel to its classified S3 key (relative to the user prefix); Warnings records
// anything that fell back to the legacy layout. Object/Date are the folder-level values the detector
// resolved, exposed so the caller can disambiguate a colliding calibration set (append the date).
type Plan struct {
	Keys     map[string]string
	Warnings []string
	Object   string
	Date     string
}

// Bucket roots (relative to the user prefix).
const (
	rootDarks   = "darks"
	rootOffsets = "offsets"
	rootFlats   = "flats"
	rootLum     = "lum"
	rootData    = "data" // legacy fallback mirror
)

// Classify places every file of the source folder (folderRel = its DataDir-relative slash path) into the
// classified layout. Object + date are detected once for the folder; a non-frame file inherits its
// directory's majority type; an unclassifiable file (or a light with no object/date) falls back to
// data/<folderRel>/<rel>.
func Classify(folderRel string, files []FileInfo) Plan {
	object := detectObject(folderRel, files)
	date := detectDate(folderRel, files)
	plan := Plan{Keys: make(map[string]string, len(files)), Object: object, Date: date}
	dirType := majorityDirTypes(files)
	for _, f := range files {
		typ := f.Type
		if typ == Unknown { // info.txt sidecars etc. inherit their dir's dominant frame type
			typ = dirType[path.Dir(f.Rel)]
		}
		key, warn := keyForFile(folderRel, object, date, typ, f.Rel)
		plan.Keys[f.Rel] = key
		if warn != "" {
			plan.Warnings = appendUnique(plan.Warnings, warn)
		}
	}
	return plan
}

// keyForFile computes one file's classified key (and a warning when it falls back to the legacy layout).
func keyForFile(folderRel, object, date string, typ FrameType, rel string) (string, string) {
	switch typ {
	case Dark:
		return calibKey(rootDarks, folderRel, date, rel), ""
	case Bias:
		return calibKey(rootOffsets, folderRel, date, rel), ""
	case Flat:
		return calibKey(rootFlats, folderRel, date, rel), ""
	case Light:
		if object == "" || date == "" {
			return legacyKey(folderRel, rel), fmt.Sprintf("light %q has no detectable object/date — kept under data/", rel)
		}
		return path.Join(rootLum, object, date, rel), "" // filter subdirs (…/L/…) survive verbatim
	default:
		return legacyKey(folderRel, rel), fmt.Sprintf("could not classify %q — kept under data/", rel)
	}
}

// calibKey places a calibration file under root/<set>/<basename>, where <set> is the file's immediate
// parent dir name — keeping a signature dir like "darks_0gain_300s_-25deg" — with the date appended when
// the set name is generic (so two nights' plain "darks/" folders don't collide into one set).
func calibKey(root, folderRel, date, rel string) string {
	set := leaf(path.Dir(rel))
	if set == "" || set == "." {
		set = leaf(folderRel)
	}
	if isGenericDir(set) {
		set = set + "_" + date
	}
	return path.Join(root, set, path.Base(rel))
}

// legacyKey is the reversible mirror key (data/<folderRel>/<rel>) used when classification is impossible.
func legacyKey(folderRel, rel string) string {
	return path.Join(rootData, folderRel, rel)
}

// majorityDirTypes returns, per directory, the most common frame type among its typed files — so a
// non-frame file (info.txt) can inherit it.
func majorityDirTypes(files []FileInfo) map[string]FrameType {
	counts := map[string]map[FrameType]int{}
	for _, f := range files {
		if f.Type == Unknown {
			continue
		}
		d := path.Dir(f.Rel)
		if counts[d] == nil {
			counts[d] = map[FrameType]int{}
		}
		counts[d][f.Type]++
	}
	out := make(map[string]FrameType, len(counts))
	for d, m := range counts {
		best, bestN := Unknown, 0
		for t, n := range m {
			if n > bestN {
				best, bestN = t, n
			}
		}
		out[d] = best
	}
	return out
}

// appendUnique adds s to warns unless already present (Classify can hit the same fallback per file).
func appendUnique(warns []string, s string) []string {
	for _, w := range warns {
		if w == s {
			return warns
		}
	}
	return append(warns, s)
}
