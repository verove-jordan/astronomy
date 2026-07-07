package optics

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// WriteArtifacts writes two review sidecars next to the master flat: a JSON summary
// "<master>.defects.json" ({"v":1,"qc":...,"defects":...}) and a grayscale preview PNG
// "<master>_defects.png" with each defect's bbox outlined in red. Soft-fails on the first I/O error.
func WriteArtifacts(masterPath string, qc FlatQC, defects []Defect) error {
	base := strings.TrimSuffix(masterPath, filepath.Ext(masterPath))
	if err := writeDefectsJSON(base+".defects.json", qc, defects); err != nil {
		return err
	}
	return writeDefectsPNG(masterPath, base+"_defects.png", defects)
}

// writeDefectsJSON marshals the QC verdict and defects (Shape omitted via json:"-") to path.
func writeDefectsJSON(path string, qc FlatQC, defects []Defect) error {
	payload := struct {
		V       int      `json:"v"`
		QC      FlatQC   `json:"qc"`
		Defects []Defect `json:"defects"`
	}{V: 1, QC: qc, Defects: defects}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// writeDefectsPNG renders the downsampled detection plane as an auto-scaled (P0.5..P99.5) grayscale
// image and outlines each defect bbox in red, mapping full-res coordinates back to the preview via
// the same `scale`.
func writeDefectsPNG(masterPath, outPath string, defects []Defect) error {
	plane, w, h, scale, _, err := LoadFlatPlane(masterPath)
	if err != nil {
		return err
	}
	sub := imgops.Subsample(plane, 200000)
	lo := imgops.Percentile(sub, 0.5)
	hi := imgops.Percentile(sub, 99.5)
	span := hi - lo
	if span <= 0 {
		span = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, v := range plane {
		g := uint8(clampF((float64(v)-lo)/span, 0, 1) * 255)
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = g, g, g, 255
	}
	red := color.RGBA{R: 255, A: 255}
	for i := range defects {
		d := &defects[i]
		drawRect(img, int(float64(d.X0)/scale), int(float64(d.Y0)/scale),
			int(float64(d.X1)/scale), int(float64(d.Y1)/scale), red)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// drawRect strokes the outline of the rectangle [x0,x1]x[y0,y1] (clamped to the image) in col.
func drawRect(img *image.RGBA, x0, y0, x1, y1 int, col color.RGBA) {
	b := img.Bounds()
	x0, x1 = clampInt(x0, 0, b.Dx()-1), clampInt(x1, 0, b.Dx()-1)
	y0, y1 = clampInt(y0, 0, b.Dy()-1), clampInt(y1, 0, b.Dy()-1)
	for x := x0; x <= x1; x++ {
		img.SetRGBA(x, y0, col)
		img.SetRGBA(x, y1, col)
	}
	for y := y0; y <= y1; y++ {
		img.SetRGBA(x0, y, col)
		img.SetRGBA(x1, y, col)
	}
}
