package planetary

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG decoder for image.Decode
	_ "image/png"  // register PNG decoder for image.Decode
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/image/tiff"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// apEstimateWinFloor bounds the minimum-detail window: below ~24 px a "detail cell" is seeing
// noise, and the per-axis grid caps at apAlignPointsGridMax regardless.
const apEstimateWinFloor = 24

// rawStillExts are camera raws the estimator rejects (the pipeline Siril/sips-develops those; the
// estimator stays develop-free).
var rawStillExts = map[string]bool{
	".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true,
	".dng": true, ".heic": true, ".heif": true,
}

// AlignPointsEstimate reports how many dense alignment points a frame supports at a given minimum
// window size — the payload of POST /api/planetary/align-points.
type AlignPointsEstimate struct {
	Frame                string   `json:"frame"` // chosen source path — set by the API layer
	Width                int      `json:"width"`
	Height               int      `json:"height"`
	WindowPx             int      `json:"window_px"`              // effective min-detail window (native px)
	CellPx               int      `json:"cell_px"`                // min(W,H)/PerAxis
	PerAxis              int      `json:"per_axis"`               // clamp(minDim/WindowPx, 10, 48)
	TotalPoints          int      `json:"total_points"`           // PerAxis²
	UsablePoints         int      `json:"usable_points"`          // lit + structured (the run's own veto recipe)
	UsableFraction       float64  `json:"usable_fraction"`        // UsablePoints/TotalPoints
	SuggestedAlignPoints int      `json:"suggested_align_points"` // = TotalPoints — pastes into align_points
	AutoPerAxis          int      `json:"auto_per_axis"`          // denseGridN auto, for comparison
	Disc                 DiscInfo `json:"disc"`
}

// DiscInfo is the fitted lunar limb circle (full-res px); OK=false = no confident disc — the
// estimate still stands via the lit-surface mask.
type DiscInfo struct {
	CX float64 `json:"cx"`
	CY float64 `json:"cy"`
	R  float64 `json:"r"`
	OK bool    `json:"ok"`
}

// EstimateAlignPoints lays a grid sized to minWinPx (0 = the dense pass's default ~4%-of-min-dim
// window) and counts the nodes the real dense aligner would keep. It is synchronous and cheap
// (~one extra full-frame pass on a 16 MP frame).
func EstimateAlignPoints(im *fits.Image, minWinPx int) AlignPointsEstimate {
	minDim := min(im.W, im.H)
	winPx := minWinPx
	if winPx <= 0 {
		winPx = 2 * minDim * apDensePatchPct / 100 // the dense pass's full patch width (~4% of min dim)
	}
	if winPx < apEstimateWinFloor {
		winPx = apEstimateWinFloor
	}
	n := minDim / winPx
	if n < apGridN {
		n = apGridN
	}
	if n > apAlignPointsGridMax {
		n = apAlignPointsGridMax
	}
	cx, cy := apCenters(im.W, im.H, n)
	usable := countStructuredOnDisk(im, cx, cy, winPx/2)
	fit, ok := fitLunarDisc(im)
	return AlignPointsEstimate{
		Width: im.W, Height: im.H, WindowPx: winPx, CellPx: minDim / n,
		PerAxis: n, TotalPoints: n * n, UsablePoints: usable,
		UsableFraction:       float64(usable) / float64(n*n),
		SuggestedAlignPoints: n * n, AutoPerAxis: denseGridN(im.W, im.H),
		Disc: DiscInfo{CX: fit.CX, CY: fit.CY, R: fit.R, OK: ok},
	}
}

// countStructuredOnDisk counts AP nodes that sit on the lit disc AND carry enough texture to
// correlate — the exact gate the real dense pass applies (apDiskMask, then regionLaplacianVariance
// ≥ apDenseMinStructure on the warp-blurred plane), so the count predicts what the run keeps.
func countStructuredOnDisk(im *fits.Image, cx, cy []float64, r int) int {
	onDisk := apDiskMask(im, cx, cy)
	blur := blurPlane(im, warpBlur)
	usable := 0
	for k, on := range onDisk {
		if !on {
			continue
		}
		if regionLaplacianVariance(blur.Pix[0], im.W, im.H,
			int(cx[k])-r, int(cy[k])-r, 2*r, 2*r) >= apDenseMinStructure {
			usable++
		}
	}
	return usable
}

// LoadLuminanceFrame reads a still as a 1-plane luminance fits.Image for the estimator: FITS
// directly (first plane, like the whole pipeline); PNG/TIFF/JPEG via the Go image stack (16-bit
// aware, RGB→mean luminance). Camera raws are rejected.
func LoadLuminanceFrame(path string) (*fits.Image, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case fitsExts[ext]:
		return fits.ReadImage(path)
	case rawStillExts[ext]:
		return nil, fmt.Errorf("camera-raw stills are not supported by the estimator; use a FITS/TIFF frame or a video")
	default:
		img, err := decodeStill(path, ext)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
		}
		return imageToLuminance(img), nil
	}
}

// decodeStill decodes a TIFF/PNG/JPEG file into an in-memory image.Image.
func decodeStill(path, ext string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if ext == ".tif" || ext == ".tiff" {
		return tiff.Decode(f)
	}
	img, _, derr := image.Decode(f)
	return img, derr
}

// imageToLuminance flattens a decoded image to a single 0..1 luminance plane (mean of RGB, or the
// gray value for mono). The estimator's stats are scale-invariant, so the exact scaling is moot.
func imageToLuminance(img image.Image) *fits.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := fits.NewImage(w, h, 1)
	p := out.Pix[0]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA() // 16-bit; gray → r==g==b
			p[y*w+x] = float32(r+g+bl) / (3 * 65535)
		}
	}
	return out
}

// ExtractFirstFrame writes video's first frame as destDir/first.png — the same 16-bit pix_fmt
// probe as extractFrames plus "-frames:v 1" so a multi-GB capture extracts one image in well under
// a second — and returns the PNG path.
func ExtractFirstFrame(ctx context.Context, ffmpegBin, video, destDir string) (string, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	pixFmt := pngPixFmtFor(videoPixFmt(ctx, ffprobeBinFor(ffmpegBin), video))
	out := filepath.Join(destDir, "first.png")
	args := []string{"-y", "-i", video}
	if pixFmt != "" {
		args = append(args, "-pix_fmt", pixFmt)
	}
	args = append(args, "-frames:v", "1", out)
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	if o, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg first frame: %w\n%s", err, lastLines(string(o), 5))
	}
	return out, nil
}
