package solarsystem

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/buildinfo"
)

// The manifest: everything the browser needs to draw and animate the system without asking again.
//
// It is deliberately the whole model rather than a sampled trajectory. Handing over the elements
// means the page can be scrubbed across two and a half centuries at sixty frames a second with no
// round trip, and it means the browser is running the engine's arithmetic rather than interpolating
// the engine's answers.

// ManifestVersion is the shape of the payload. The browser refuses a version it does not know rather
// than misreading one, the same contract the star field and the galaxy cloud use.
const ManifestVersion = 1

// Manifest is the served description of the scene.
type Manifest struct {
	Version int    `json:"version"`
	Engine  string `json:"engine"`

	// RangeFrom and RangeTo are the years the orbital model is defended over. The page refuses to
	// scrub outside them rather than drawing positions nothing stands behind.
	RangeFrom int `json:"range_from"`
	RangeTo   int `json:"range_to"`

	AUPerKm float64 `json:"au_per_km"`
	Bodies  []Body  `json:"bodies"`

	// Textures lists the texture keys actually present on this engine. A key that is absent is not an
	// error: the body falls back to procedural shading, exactly as a missing StarNet++ falls back to
	// full stars.
	Textures []string `json:"textures"`

	Sources []Source `json:"sources"`
}

// Source is one dataset the scene is built from, with the attribution it carries.
type Source struct {
	Name    string `json:"name"`
	Covers  string `json:"covers"`
	Licence string `json:"licence"`
	URL     string `json:"url,omitempty"`
}

// dataSources is the attribution the page shows. Every number in the manifest comes from one of
// these, and the two that require attribution by licence are named on screen, not just here.
func dataSources() []Source {
	return []Source{{
		Name:    "JPL Solar System Dynamics — approximate positions of the major planets",
		Covers:  "planet and Pluto orbits, 1800–2050",
		Licence: "public domain (NASA/JPL-Caltech)",
		URL:     "https://ssd.jpl.nasa.gov/planets/approx_pos.html",
	}, {
		Name:    "IAU/IAG Working Group on Cartographic Coordinates and Rotational Elements (2015)",
		Covers:  "pole directions, axial tilts and rotation rates",
		Licence: "published report",
	}, {
		Name:    "IAU 2015 nominal values and the JPL planetary fact sheets",
		Covers:  "radii, masses and albedos",
		Licence: "public domain (NASA/JPL-Caltech)",
	}, {
		Name:    "Astronomical Almanac low-precision formulae",
		Covers:  "the Moon's position and distance",
		Licence: "published tables",
	}, {
		Name:    "HYG v4.1 (astronexus) + Stellarium constellation lines",
		Covers:  "the background star field",
		Licence: "CC BY-SA 4.0 / GPL",
	}}
}

var (
	manifestOnce sync.Once
	manifestJSON []byte
	manifestETag string
	manifestErr  error
)

// Build assembles the manifest for this engine. The body table and the source list are compiled in,
// so the only thing that can change between calls is which textures are on disk — which is why the
// result is cached per texture set rather than for the process's lifetime.
func Build(textures []string) Manifest {
	return Manifest{
		Version:   ManifestVersion,
		Engine:    buildinfo.String(),
		RangeFrom: astro.ElementsFrom,
		RangeTo:   astro.ElementsTo,
		AUPerKm:   AUPerKm,
		Bodies:    All(),
		Textures:  textures,
		Sources:   dataSources(),
	}
}

// Encoded returns the manifest as JSON with a strong ETag derived from the bytes themselves, so a
// reload after an engine upgrade re-fetches and an unchanged engine answers 304.
func Encoded(textures []string) (data []byte, etag string, err error) {
	// One texture set per process in practice: the directory is scanned once at startup. Guarding it
	// with sync.Once keeps the common case free without pretending to be a general cache.
	manifestOnce.Do(func() {
		manifestJSON, manifestErr = json.Marshal(Build(textures))
		if manifestErr == nil {
			sum := sha256.Sum256(manifestJSON)
			manifestETag = fmt.Sprintf("%q", fmt.Sprintf("ss-%d-%x", ManifestVersion, sum[:8]))
		}
	})
	return manifestJSON, manifestETag, manifestErr
}
