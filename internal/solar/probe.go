package solar

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register the JPEG decoder for image.Decode
	_ "image/png"  // register the PNG decoder for image.Decode
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/tiff"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/rawconv"
)

// probeMaxEdge bounds the long edge of the image every measurement runs on. A 2048 px thumbnail of
// a 48 MP HEIC decodes in a fraction of the time the full frame would, and the limb fit reaches
// sub-pixel accuracy well below that size — the measured radius is scaled back to full resolution
// afterwards, so groups stay comparable no matter what each source's native size was.
const probeMaxEdge = 2048

// satLevel is "this pixel is clipped" in the developed, display-referred probe image. Measuring on
// the developed frame rather than the raw is deliberate for triage: a blown disc is unusable
// whether or not the sensor itself saturated.
const satLevel = 0.995

// FrameProbe is everything triage learns about one capture file without committing to a full
// ingest. Measured geometry is in FULL-resolution pixels of the source, so probes taken at
// different thumbnail sizes stay directly comparable.
type FrameProbe struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`

	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	ISO             int64  `json:"iso,omitempty"`
	ExposureMs      int64  `json:"exposure_ms,omitempty"`
	FocalLength35mm int    `json:"focal_length_35mm,omitempty"`
	CameraModel     string `json:"camera_model,omitempty"`
	TakenAtMs       int64  `json:"taken_at_ms,omitempty"`

	Video *VideoInfo `json:"video,omitempty"`

	Disc   Limb `json:"disc"`
	DiscOK bool `json:"disc_ok"`
	// ArcsecPerPx is the physical plate scale S☉/R — the only unit in which an afocal iPhone frame
	// and an ASI frame are comparable at all.
	ArcsecPerPx  float64 `json:"arcsec_per_px,omitempty"`
	ClippedFrac  float64 `json:"clipped_frac"`
	OnDiscMedian float64 `json:"on_disc_median"`
	Detail       float64 `json:"detail"`     // normalised on-disc gradient energy; higher = sharper
	LimbRatio    float64 `json:"limb_ratio"` // median(0.5R) / median(0.9R): the limb-darkening shape
	DiscFill     float64 `json:"disc_fill"`  // disc area / frame area

	Err string `json:"error,omitempty"` // probe failure; the file is reported, never silently dropped
}

// Kind classifies a capture source.
type Kind string

const (
	KindStill Kind = "still"
	KindVideo Kind = "video"
)

// sunAngularDiameterArcsec is the Sun's mean apparent diameter. The ±1.7% annual variation is far
// below any grouping tolerance, so a constant is honest here; the exact value would come from
// internal/astro if we ever needed absolute photometry.
const sunAngularDiameterArcsec = 1919.3

// probeStill measures one still (FITS, TIFF/PNG/JPEG, or a camera raw such as iPhone DNG/HEIC).
func probeStill(ctx context.Context, path, scratch string, meta fileMeta, twoBody bool) FrameProbe {
	p := FrameProbe{Path: path, Kind: KindStill}
	applyMeta(&p, meta)
	im, scale, err := loadStillProbe(ctx, path, scratch, meta)
	if err != nil {
		p.Err = err.Error()
		return p
	}
	measure(&p, im, scale, twoBody)
	return p
}

// loadStillProbe returns a reduced-resolution luminance plane plus the factor that converts its
// pixel units back to the source's full resolution.
func loadStillProbe(ctx context.Context, path, scratch string, meta fileMeta) (*fits.Image, float64, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if fitsExts[ext] {
		im, err := fits.ReadImage(path)
		if err != nil {
			return nil, 0, err
		}
		small, f := boxDownTo(im, probeMaxEdge) // already linear — a FITS frame carries sensor counts
		return small, f, nil
	}
	// Everything else — HEIC, DNG, other raws, TIFF/PNG/JPEG — goes through the same thumbnailer the
	// rest of the engine uses, which is `sips` on macOS and dcraw elsewhere.
	dst := filepath.Join(scratch, sanitizeName(path)+".png")
	if err := rawconv.Thumbnail(ctx, path, dst, probeMaxEdge); err != nil {
		return nil, 0, fmt.Errorf("thumbnail %s: %w", filepath.Base(path), err)
	}
	defer os.Remove(dst)
	im, err := decodeLuminance(dst)
	if err != nil {
		return nil, 0, err
	}
	// Both developers emit display-referred sRGB (`sips`, and dcraw's `-g 2.4 12.92`). Undo it, so a
	// still and a clip of the same Sun are measured in the same linear space and their brightness and
	// sharpness figures stay comparable when they land in the same group.
	linearizeSDRGamma(im.Pix[0])
	scale := 1.0
	if meta.Width > 0 && im.W > 0 {
		scale = float64(meta.Width) / float64(im.W)
	}
	return im, scale, nil
}

// measure fills the measured half of a probe from a reduced-resolution plane, converting geometry
// back to full-resolution pixels with scale.
func measure(p *FrameProbe, im *fits.Image, scale float64, twoBody bool) {
	if scale <= 0 {
		scale = 1
	}
	g, ok := fitGeometry(im, twoBody)
	d := g.Sun
	if !ok {
		p.DiscOK = false // most often a frame zoomed past the limb; the fit refuses rather than guessing
		return
	}
	p.DiscOK = true
	p.Disc = Limb{
		CX: d.CX * scale, CY: d.CY * scale, R: d.R * scale,
		ArcDeg: d.ArcDeg, ResidRMS: d.ResidRMS * scale, Points: d.Points, Partial: d.Partial,
	}
	if p.Disc.R > 0 {
		p.ArcsecPerPx = sunAngularDiameterArcsec / (2 * p.Disc.R)
	}
	st := discStats(im, d.CX, d.CY, d.R)
	p.ClippedFrac, p.OnDiscMedian, p.Detail, p.LimbRatio = st.clipped, st.median, st.detail, st.limbRatio
	p.DiscFill = math.Pi * d.R * d.R / float64(im.W*im.H)
}

// applyMeta copies the metadata half onto a probe.
func applyMeta(p *FrameProbe, m fileMeta) {
	p.Width, p.Height = m.Width, m.Height
	p.ISO, p.ExposureMs = m.ISO, m.ExposureMs
	p.FocalLength35mm, p.CameraModel, p.TakenAtMs = m.FocalLength35mm, m.CameraModel, m.TakenAtMs
}

// decodeLuminance reads a still into a single 0..1 luminance plane. For an Hα capture the red
// channel carries essentially all the signal, but the mean is still overwhelmingly dominated by the
// disc, and every triage measurement is a ratio — so the simple mean is enough here. The ingest
// path, where absolute channel choice matters, picks the channel explicitly.
func decodeLuminance(path string) (*fits.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var img image.Image
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".tif" || ext == ".tiff" {
		img, err = tiff.Decode(f)
	} else {
		img, _, err = image.Decode(f)
	}
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	b := img.Bounds()
	out := fits.NewImage(b.Dx(), b.Dy(), 1)
	p := out.Pix[0]
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			p[y*b.Dx()+x] = float32(r+g+bl) / (3 * 65535)
		}
	}
	return out, nil
}

// sanitizeName turns a path into a scratch-safe basename.
func sanitizeName(path string) string {
	base := filepath.Base(path)
	return strings.NewReplacer(".", "_", " ", "_", "/", "_").Replace(base)
}

var fitsExts = map[string]bool{".fits": true, ".fit": true, ".fts": true}
