package nightscape

// transients.go keeps what the clean stack rejects.
//
// computeCleanSkyStack builds the sky by sigma-clipping each pixel across the frames, which is what
// makes the result clean — and it is also, precisely, the step that deletes every meteor in a
// session. A meteor crosses in ONE frame, so it is always the outlier, and the clip is right to keep
// it out of the average. What was wrong was throwing it away afterwards.
//
// So the high-side rejections are kept: per pixel, the brightest frame that was clipped, and which
// frame it was. Brightest and not averaged, because a meteor's whole brightness lives in one frame;
// averaging it across the rest is the same as deleting it, only slower. The frame index is what lets
// a meteor be told from a satellite later — a satellite comes back in the next frame with its track
// advanced, and a meteor never does.

import (
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Transients is the layer the clean sky stack left out.
type Transients struct {
	// Img holds, per pixel, the REJECTING frame's own values — so a meteor renders at the brightness
	// it actually had, not a fraction of it.
	Img *fits.Image
	// Excess is how far above the clip that frame's luminance sat. It is the detection statistic:
	// unlike raw brightness it is already relative to what the sky does at that pixel.
	Excess []float32
	// Frame is which frame supplied the pixel, or -1 where nothing was ever rejected.
	Frame []int32
	// Count is how many frames were rejected high at this pixel. One is the signature of a meteor;
	// several usually means a hot pixel or a satellite that came back.
	Count []int32
}

func newTransients(w, h, c int) *Transients {
	t := &Transients{
		Img:    fits.NewImage(w, h, c),
		Excess: make([]float32, w*h),
		Frame:  make([]int32, w*h),
		Count:  make([]int32, w*h),
	}
	for i := range t.Frame {
		t.Frame[i] = -1
	}
	return t
}

// observe records that frame idx was rejected high at pixel i by excess.
func (t *Transients) observe(i, idx int, excess float32, frame *fits.Image) {
	if t == nil {
		return
	}
	t.Count[i]++
	if excess <= t.Excess[i] {
		return
	}
	t.Excess[i] = excess
	t.Frame[i] = int32(idx)
	for ch := 0; ch < t.Img.C && ch < frame.C; ch++ {
		t.Img.Pix[ch][i] = frame.Pix[ch][i]
	}
}

// transientFile and transientMetaFile are what a run leaves behind for the meteor pass.
const (
	transientFile     = "transients.fits"
	transientMetaFile = "transients_meta.fits"
)

// crop takes the same box the sky was cropped to. A transient layer that does not line up with the
// sky it came from is worse than none: every position measured in it would be quietly wrong.
func (t *Transients) crop(w, h int, b box) *Transients {
	if t == nil {
		return nil
	}
	out := &Transients{
		Img:    cropImage(t.Img, b),
		Excess: cropPlane(t.Excess, w, h, b),
		Frame:  cropInt32(t.Frame, w, h, b),
		Count:  cropInt32(t.Count, w, h, b),
	}
	return out
}

func cropInt32(p []int32, w, h int, b box) []int32 {
	f := make([]float32, len(p))
	for i, v := range p {
		f[i] = float32(v)
	}
	c := cropPlane(f, w, h, b)
	out := make([]int32, len(c))
	for i, v := range c {
		out[i] = int32(v)
	}
	return out
}

// write persists the layer and its per-pixel frame/count planes, and returns a note on failure.
//
// The metadata goes in a second FITS rather than extra channels of the first so the transient image
// stays a plain RGB image that any viewer opens. Frame indices and counts are small integers, which
// float32 holds exactly.
func (t *Transients) write(dir string) string {
	if t == nil || t.Img == nil {
		return ""
	}
	if err := t.Img.WriteFITS(filepath.Join(dir, transientFile)); err != nil {
		return "transient layer not written: " + err.Error()
	}
	meta := fits.NewImage(t.Img.W, t.Img.H, 3)
	for i := range t.Frame {
		meta.Pix[0][i] = float32(t.Frame[i])
		meta.Pix[1][i] = float32(t.Count[i])
		meta.Pix[2][i] = t.Excess[i]
	}
	if err := meta.WriteFITS(filepath.Join(dir, transientMetaFile)); err != nil {
		return "transient metadata not written: " + err.Error()
	}
	return ""
}
