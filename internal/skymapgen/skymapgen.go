// Package skymapgen builds the compact star + constellation-line dataset the frontend sky map ships.
// It fetches the public HYG star catalogue and Stellarium's western constellation figures at BUILD time
// (network here only), filters to a magnitude limit, and writes a small JSON the app loads offline.
package skymapgen

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

// Default source URLs. HYG is pinned to its CURRENT build; Stellarium is pinned to a tag that still ships
// the classic `.fab` constellation format. Both are re-fetched only when a maintainer runs the generator.
const (
	DefaultHYGURL   = "https://raw.githubusercontent.com/astronexus/HYG-Database/main/hyg/CURRENT/hygdata_v41.csv"
	DefaultLinesURL = "https://raw.githubusercontent.com/Stellarium/stellarium/v0.21.3/skycultures/western/constellationship.fab"
	DefaultNamesURL = "https://raw.githubusercontent.com/Stellarium/stellarium/v0.21.3/skycultures/western/constellation_names.eng.fab"
	DefaultMagLimit = 6.0
	DefaultOutPath  = "frontend/src/assets/skymap.json"
)

// Options configures a dataset build.
type Options struct {
	HYGURL   string
	LinesURL string
	NamesURL string
	MagLimit float64
	OutPath  string
}

// withDefaults fills any unset option with its default.
func (o Options) withDefaults() Options {
	if o.HYGURL == "" {
		o.HYGURL = DefaultHYGURL
	}
	if o.LinesURL == "" {
		o.LinesURL = DefaultLinesURL
	}
	if o.NamesURL == "" {
		o.NamesURL = DefaultNamesURL
	}
	if o.MagLimit == 0 {
		o.MagLimit = DefaultMagLimit
	}
	if o.OutPath == "" {
		o.OutPath = DefaultOutPath
	}
	return o
}

// nameEntry marshals to the compact ["index","name"] pair.
type nameEntry struct {
	Index int
	Name  string
}

func (n nameEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{n.Index, n.Name})
}

// conEntry is a constellation label placed at its figure's centroid.
type conEntry struct {
	Name   string  `json:"name"`
	RADeg  float64 `json:"ra"`
	DecDeg float64 `json:"dec"`
}

// skyMap is the emitted JSON. Stars are [raDeg, decDeg, mag]; lines index into stars.
type skyMap struct {
	MagLimit       float64      `json:"magLimit"`
	Source         string       `json:"source"`
	Stars          [][3]float64 `json:"stars"`
	Names          []nameEntry  `json:"names"`
	Lines          [][2]int     `json:"lines"`
	Constellations []conEntry   `json:"constellations"`
}

// Generate fetches the sources, assembles the dataset, and writes it to opts.OutPath.
func Generate(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()

	stars, byHip, err := fetchStars(ctx, opts.HYGURL, opts.MagLimit)
	if err != nil {
		return fmt.Errorf("fetch stars: %w", err)
	}
	segments, err := fetchConstellationLines(ctx, opts.LinesURL)
	if err != nil {
		return fmt.Errorf("fetch constellation lines: %w", err)
	}
	names, err := fetchConstellationNames(ctx, opts.NamesURL)
	if err != nil {
		return fmt.Errorf("fetch constellation names: %w", err)
	}

	out := assemble(stars, byHip, segments, names, opts.MagLimit)
	return write(opts.OutPath, out)
}

// assemble unions the magnitude-filtered stars with any star referenced by a constellation line (so
// figures never break), resolves segments to star indices, and builds labels + constellation centroids.
func assemble(kept []rawStar, byHip map[int]rawStar, segments []segment, names map[string]string, magLimit float64) skyMap {
	final := append([]rawStar(nil), kept...)
	hipIndex := map[int]int{}
	for i, s := range final {
		if s.HIP > 0 {
			hipIndex[s.HIP] = i
		}
	}
	// Pull in fainter stars only because a constellation line needs them.
	ensure := func(hip int) {
		if hip <= 0 {
			return
		}
		if _, ok := hipIndex[hip]; ok {
			return
		}
		s, ok := byHip[hip]
		if !ok {
			return
		}
		hipIndex[hip] = len(final)
		final = append(final, s)
	}
	for _, seg := range segments {
		ensure(seg.A)
		ensure(seg.B)
	}

	sky := skyMap{
		MagLimit: magLimit,
		Source:   "HYG v4.1 (astronexus, CC-BY-SA) + Stellarium western constellationship (GPL)",
	}
	for _, s := range final {
		sky.Stars = append(sky.Stars, [3]float64{round(s.RADeg, 3), round(s.DecDeg, 3), round(s.Mag, 2)})
		if s.Name != "" {
			// index is the position just appended
			sky.Names = append(sky.Names, nameEntry{Index: len(sky.Stars) - 1, Name: s.Name})
		}
	}
	for _, seg := range segments {
		a, okA := hipIndex[seg.A]
		b, okB := hipIndex[seg.B]
		if okA && okB && a != b {
			sky.Lines = append(sky.Lines, [2]int{a, b})
		}
	}
	sky.Constellations = constellationLabels(segments, hipIndex, final, names)
	return sky
}

// constellationLabels places each constellation's English name at the centroid (unit-vector mean, so RA
// wrap is handled) of the stars its figure uses.
func constellationLabels(segments []segment, hipIndex map[int]int, stars []rawStar, names map[string]string) []conEntry {
	type acc struct{ x, y, z float64 }
	sums := map[string]*acc{}
	for _, seg := range segments {
		for _, hip := range []int{seg.A, seg.B} {
			idx, ok := hipIndex[hip]
			if !ok {
				continue
			}
			s := stars[idx]
			a := sums[seg.Con]
			if a == nil {
				a = &acc{}
				sums[seg.Con] = a
			}
			ra, dec := s.RADeg*deg2rad, s.DecDeg*deg2rad
			a.x += math.Cos(dec) * math.Cos(ra)
			a.y += math.Cos(dec) * math.Sin(ra)
			a.z += math.Sin(dec)
		}
	}
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []conEntry
	for _, abbr := range keys {
		a := sums[abbr]
		ra := norm360(math.Atan2(a.y, a.x) * rad2deg)
		dec := math.Atan2(a.z, math.Hypot(a.x, a.y)) * rad2deg
		name := names[abbr]
		if name == "" {
			name = abbr
		}
		out = append(out, conEntry{Name: name, RADeg: round(ra, 2), DecDeg: round(dec, 2)})
	}
	return out
}

func write(path string, out skyMap) error {
	b, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("skymap: %d stars, %d line segments, %d constellations, %d labels → %s (%d bytes)\n",
		len(out.Stars), len(out.Lines), len(out.Constellations), len(out.Names), path, len(b))
	return nil
}

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi
)

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

func norm360(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}
