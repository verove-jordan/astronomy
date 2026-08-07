// Package scene3d turns a completed run's star annotation into a three-dimensional scene: every
// detected star placed along its own line of sight at its own distance, and every catalogued object
// in the field hung at the distance it actually sits at. The result is the run's photograph as a
// volume — foreground stars, the target, and the background field separated in depth.
//
// It is a pure function of annotate.Result plus the run's final image: no FITS, no WCS, no Siril.
// Everything that needs the plate solution (the sky position of each star, the image's anchoring on
// the sky, the projected footprint of each object) was already settled by internal/annotate, in the
// one place where the validated solution and the crop mapping live. That seam is deliberate — it
// keeps the geometry from being derived twice and disagreeing, and it makes this package testable
// from a literal.
//
// Output is <runDir>/scene3d.json (a small manifest) plus <runDir>/scene3d.bin (the star field, 32
// bytes per star, laid out for a single GPU upload) and <runDir>/scene3d_bg.png (the billboard
// texture). All of it is computed once and cached beside the run's other outputs.
package scene3d

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/annotate"
	"github.com/verove-jordan/astronomy/internal/buildinfo"
)

// File names written beside the run's outputs.
const (
	manifestFileName = "scene3d.json"
	binFileName      = "scene3d.bin"
)

// ManifestVersion moves with the binary's fileVersion. A cached scene from an older version is
// rebuilt rather than served: the viewer's decoder refuses a record layout it does not recognise, so
// handing it a stale file would fail in the browser instead of here.
const ManifestVersion = 2

// Reasons a run has no 3D scene. They are surfaced verbatim so the UI can explain the absence of
// the view rather than just hiding it.
const (
	reasonUnsolved    = "the field has no validated astrometric solution"
	reasonNoFrame     = "the run predates the scene frame — recompute its stars"
	reasonNoStars     = "no detected star could be given a distance"
	reasonBadGeometry = "the field geometry is degenerate"
)

// Options configure one scene build.
type Options struct {
	RunDir string
	// Locate resolves a run-relative file to a local absolute path (the API wires the S3
	// ensureServable pull-through here); nil → plain os.Stat inside RunDir.
	Locate func(rel string) (string, bool)
	Now    func() time.Time // test seam; nil → time.Now
}

// Manifest is scene3d.json: everything the viewer needs that is not per-star. Small by design — the
// bulk rides in the binary.
type Manifest struct {
	Version    int    `json:"version"`
	Engine     string `json:"engine,omitempty"`
	ComputedAt string `json:"computed_at"`
	// Available is false when the run cannot have a 3D scene at all; Reason then says why, so the UI
	// can explain itself instead of silently dropping the view.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// NeedsRecompute distinguishes "this run can never have a scene" from "its annotation is too old
	// to build one from". The second is fixable by re-running the star annotation, so the UI offers
	// that instead of leaving the view mysteriously absent.
	NeedsRecompute bool `json:"needs_recompute,omitempty"`

	Image       Dims        `json:"image"`
	Camera      Camera      `json:"camera"`
	Depth       DepthRange  `json:"depth"`
	Stars       StarCounts  `json:"stars"`
	Photometric Photometric `json:"photometric"`
	Billboards  []Billboard `json:"billboards,omitempty"`
	// Points/Backdrop are run-relative file names the viewer fetches.
	Points   string `json:"points,omitempty"`
	Backdrop string `json:"backdrop,omitempty"`
}

// Dims is the final image's pixel size — the space every footprint in this manifest is measured in.
type Dims struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Camera describes the pinhole that reproduces the photograph. A TAN plate solution IS a pinhole
// projection, so a camera at the origin looking down +Z with these half-field tangents renders the
// star field back into exactly the picture it came from — which is what the depth slider opens from.
type Camera struct {
	TanHalfW    float64 `json:"tan_half_w"`
	TanHalfH    float64 `json:"tan_half_h"`
	FovYDeg     float64 `json:"fov_y_deg"`
	CenterRA    float64 `json:"center_ra"`
	CenterDec   float64 `json:"center_dec"`
	RightHanded bool    `json:"right_handed"` // false on a mirrored field (a star-diagonal train)
}

// DepthRange is the field's distance structure, in parsec. Near/Far are what the viewer's
// logarithmic depth warp spans, and they must cover EVERYTHING the scene draws — every placed star
// and every catalogued object.
//
// That last part is not obvious and getting it wrong is invisible in the data and glaring on screen.
// The warp clamps anything outside its range onto the end plane, so a range taken from the stars
// alone puts a galaxy at 7 Mpc on exactly the same plane as a star at 600 pc: twelve thousand times
// nearer, drawn shoulder to shoulder. The range is therefore the stars' own 1st-to-99th percentile
// spread — trimmed at the ends so one wild photometric estimate cannot set the scale — widened to
// take in every object's distance.
//
// The mapping from these to scene units is deliberately NOT here: how deep the cone should look is a
// view preference, not a measurement, and it belongs to whoever is drawing.
type DepthRange struct {
	NearPc   float64 `json:"near_pc"`
	FarPc    float64 `json:"far_pc"`
	MinPc    float64 `json:"min_pc"`
	MaxPc    float64 `json:"max_pc"`
	MedianPc float64 `json:"median_pc"`
}

// StarCounts breaks the scene down by how well it is known.
type StarCounts struct {
	Plotted    int `json:"plotted"`    // detected stars the annotation shipped
	Placed     int `json:"placed"`     // of those, the ones that got a distance and are in the binary
	Measured   int `json:"measured"`   // placed by a catalogue parallax
	Estimated  int `json:"estimated"`  // placed by this frame's photometry
	Unknown    int `json:"unknown"`    // no distance at all — NOT in the binary
	Identified int `json:"identified"` // matched to a catalogue entry
	Named      int `json:"named"`      // carries a designation in the name table
	// PhysicalColour counts stars drawn at their blackbody hue rather than at the sampled pixel
	// colour; Moving counts those with a real measured space velocity.
	PhysicalColour int `json:"physical_colour"`
	Moving         int `json:"moving"`
}

// Billboard is one catalogued object placed at its distance, as a quad cut from the backdrop by its
// own projected footprint. Everything but the distance is in final-image pixels, so the viewer needs
// no astronomy to place it — the footprint and the camera between them fix where it hangs.
type Billboard struct {
	Name       string  `json:"name"`
	Secondary  string  `json:"secondary,omitempty"`
	Type       string  `json:"type,omitempty"`
	DistPc     float64 `json:"dist_pc"`
	DistSource string  `json:"dist_source"` // measured (from this frame's member stars) | table
	// TableDistPc is the catalogued value when the distance was measured instead. Both are kept so a
	// disagreement between the picture and the reference is visible rather than quietly resolved.
	TableDistPc float64 `json:"table_dist_pc,omitempty"`
	Members     int     `json:"members,omitempty"`
	SigmaDex    float64 `json:"sigma_dex,omitempty"`

	// The footprint, in final-image pixels, exactly as annotate projected it. The object's line of
	// sight is NOT shipped: it follows from this centre and the camera, and deriving it any other way
	// (from a nearby star, say) lets the quad drift off the pixels it is cut from.
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	RXpx     float64 `json:"rx_px"`
	RYpx     float64 `json:"ry_px"`
	AngleRad float64 `json:"angle_rad"`
	// Shape is how the object occupies space, when enough is known to say. Absent means the flat
	// plane — always right at depth zero, and never claiming more than the image itself.
	Shape *Shape `json:"shape,omitempty"`
}

// Build turns one annotated run into its 3D scene and writes the three artifacts. A run that cannot
// have a scene is not an error: the manifest comes back with Available false and a reason, is still
// persisted, and the viewer explains itself.
func Build(res *annotate.Result, o Options) (*Manifest, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Manifest{
		Version:    ManifestVersion,
		Engine:     buildinfo.String(),
		ComputedAt: o.Now().UTC().Format(time.RFC3339),
	}
	if res != nil {
		m.Image = Dims{Width: res.Image.Width, Height: res.Image.Height}
	}
	switch {
	case res == nil || !res.Solved:
		m.Reason = reasonUnsolved
		return m, m.write(o.RunDir)
	case res.Solve.Frame == nil:
		// The annotation predates the scene frame. Everything else about it is still good, so this is
		// a stale cache rather than an unsupported run — recomputing the stars fixes it.
		m.Reason = reasonNoFrame
		m.NeedsRecompute = true
		return m, m.write(o.RunDir)
	}

	b, err := newBasis(*res.Solve.Frame)
	if err != nil {
		m.Reason = reasonBadGeometry
		return m, m.write(o.RunDir)
	}
	m.Camera = Camera{
		TanHalfW: b.TanHalfW, TanHalfH: b.TanHalfH, FovYDeg: b.FovYDeg,
		CenterRA: res.Solve.Frame.CenterRA, CenterDec: res.Solve.Frame.CenterDec,
		RightHanded: b.RightHanded,
	}

	fit, ph := fitColour(res.Stars)
	gradeHoldout(res.Stars, fit, &ph)
	m.Photometric = ph

	depths, sources := resolveAll(res.Stars, fit)
	members := clusterMembers(res, &m.Billboards)
	stars, names, counts := encodeStars(res.Stars, depths, sources, members, b, fit)
	m.Stars = counts
	m.Stars.Plotted = len(res.Stars)
	if len(stars) == 0 {
		m.Reason = reasonNoStars
		return m, m.write(o.RunDir)
	}
	m.Depth = depthRange(stars, m.Billboards)

	if err := writeBinFile(filepath.Join(o.RunDir, binFileName), stars, names); err != nil {
		return nil, err
	}
	m.Points = binFileName

	if name, err := writeBackdrop(res, o); err != nil {
		// A missing backdrop costs the billboards their texture, not the scene its stars.
		m.Billboards = nil
	} else {
		m.Backdrop = name
	}

	m.Available = true
	return m, m.write(o.RunDir)
}

// resolveAll assigns every plotted star a distance and a provenance.
func resolveAll(points []annotate.Point, fit colourFit) ([]float64, []DepthSource) {
	depths := make([]float64, len(points))
	sources := make([]DepthSource, len(points))
	for i, p := range points {
		depths[i], sources[i] = resolveDepth(p, fit)
	}
	return depths, sources
}

// clusterMembers measures every cluster in the field from its own member stars, appends the
// billboards for every catalogued object that has a distance, and returns the set of star indices
// that belong to a measured cluster.
func clusterMembers(res *annotate.Result, out *[]Billboard) map[int]bool {
	members := map[int]bool{}
	for _, l := range res.Labels {
		if l.Kind != "dso" || l.Extent == nil {
			continue
		}
		bb := Billboard{
			Name: l.Name, Secondary: l.Secondary, Type: l.Type,
			X: l.X, Y: l.Y,
			RXpx: l.Extent.RXpx, RYpx: l.Extent.RYpx, AngleRad: l.Extent.AngleRad,
		}
		table := tableDistance(l.Name, l.Secondary)
		bb.DistPc, bb.DistSource = table, "table"

		if clusterTypes[l.Type] {
			if fit, ok := measureCluster(l, res.Stars); ok {
				bb.DistPc, bb.DistSource = fit.distPc, "measured"
				bb.TableDistPc = table
				bb.Members, bb.SigmaDex = fit.members, fit.sigmaDex
				for _, i := range fit.memberOf {
					members[i] = true
				}
			}
		}
		if bb.DistPc <= 0 {
			continue // nothing known about how far away it is → no billboard
		}
		bb.Shape = shapeFor(l, bb.DistPc)
		*out = append(*out, bb)
	}
	return members
}

// encodeStars turns the placed stars into encodable records plus the name table they index.
func encodeStars(points []annotate.Point, depths []float64, sources []DepthSource, members map[int]bool, b basis, fit colourFit) ([]star, []string, StarCounts) {
	var out []star
	var names []string
	nameIdx := map[string]uint16{}
	var c StarCounts

	for i, p := range points {
		if p.Star != nil {
			c.Identified++
		}
		if sources[i] == DepthUnknown {
			c.Unknown++
			continue
		}
		dir, ok := b.project(p.RADeg, p.DecDeg).unit()
		if !ok || dir.Z <= 0 { // behind the observer means the geometry is wrong, not that the star is
			c.Unknown++
			continue
		}

		s := star{dir: dir, distPc: depths[i], source: sources[i], srcIdx: uint16(i)}
		var catalogueCI *float64
		if p.Star != nil {
			catalogueCI = p.Star.CI
		}
		var physical bool
		s.r, s.g, s.b, physical = starColour(p.Hex, catalogueCI, fit)
		if physical {
			s.flags |= flagPhysicalColour
			c.PhysicalColour++
		}
		if v, ok := spaceVelocity(p.Star, b); ok {
			s.vel = v
			s.flags |= flagHasVelocity
			c.Moving++
		}
		if hasMag(p) {
			s.mag, s.hasMag = p.Mag, true
			// Absolute magnitude from the distance just assigned, so point size can scale with real
			// luminosity for EVERY placed star and not only the ones the catalogue described.
			s.absMag, s.hasAbsMag = p.Mag-5*math.Log10(depths[i])+5, true
		}
		if p.Star != nil {
			s.flags |= flagIdentified
			if p.Star.AbsMag != nil {
				s.absMag, s.hasAbsMag = *p.Star.AbsMag, true
			}
			if n := p.Star.Name; n != "" {
				idx, seen := nameIdx[n]
				if !seen {
					names = append(names, n)
					idx = uint16(len(names))
					nameIdx[n] = idx
				}
				s.nameIdx = idx
				c.Named++
			}
		}
		if members[i] {
			s.flags |= flagClusterMember
		}

		switch sources[i] {
		case DepthMeasured:
			c.Measured++
		case DepthEstimated:
			c.Estimated++
		}
		out = append(out, s)
	}
	c.Placed = len(out)
	return out, names, c
}

// depthRange summarises how the field is distributed in depth, and settles the span the warp runs
// over. Min/Max/Median describe the STARS; Near/Far are the drawing range and take in the objects
// too, because anything left outside them is clamped onto an end plane rather than placed.
func depthRange(stars []star, billboards []Billboard) DepthRange {
	d := make([]float64, 0, len(stars))
	for _, s := range stars {
		d = append(d, s.distPc)
	}
	sort.Float64s(d)
	at := func(q float64) float64 {
		i := int(q * float64(len(d)-1))
		return d[clampInt(i, 0, len(d)-1)]
	}
	r := DepthRange{
		NearPc: at(0.01), FarPc: at(0.99),
		MinPc: d[0], MaxPc: d[len(d)-1], MedianPc: at(0.5),
	}
	for _, b := range billboards {
		if b.DistPc <= 0 {
			continue
		}
		r.NearPc = math.Min(r.NearPc, b.DistPc)
		r.FarPc = math.Max(r.FarPc, b.DistPc)
	}
	// A degenerate span would divide by zero in the warp and put the whole field on one plane.
	if !(r.FarPc > r.NearPc) {
		r.FarPc = r.NearPc * 1.001
	}
	return r
}

// writeBinFile serialises the star field atomically, so a reader never sees a torn buffer.
func writeBinFile(path string, stars []star, names []string) error {
	return writeAtomic(path, func(f *os.File) error { return writeBin(f, stars, names) })
}

// write persists the manifest atomically.
func (m *Manifest) write(runDir string) error {
	return writeAtomic(filepath.Join(runDir, manifestFileName), func(f *os.File) error {
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal scene3d.json: %w", err)
		}
		_, err = f.Write(b)
		return err
	})
}

// writeAtomic runs emit against a temp file in the destination directory and renames it into place.
func writeAtomic(path string, emit func(*os.File) error) error {
	dir, base := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, "."+base+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", base, err)
	}
	defer os.Remove(tmp.Name())
	if err := emit(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", base, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", base, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", base, err)
	}
	return os.Rename(tmp.Name(), path)
}

// Load reads a previously computed <runDir>/scene3d.json. ok=false when absent or unreadable.
func Load(runDir string) (*Manifest, bool) {
	b, err := os.ReadFile(filepath.Join(runDir, manifestFileName))
	if err != nil {
		return nil, false
	}
	var m Manifest
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return &m, true
}
