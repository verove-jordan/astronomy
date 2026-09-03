package inspect

import (
	"image"
	"image/color"
	"os"

	// Register the still decoders with the standard image package so DecodeConfig can read a
	// TIFF/PNG/JPEG header. Blank imports: we only ever call image.DecodeConfig.
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/tiff"
)

// stillChannels reports how many colour planes a non-FITS still carries — 1 for monochrome, 3 for
// colour — by reading only its header (image.DecodeConfig), never its pixels. Returns 0 when the
// file cannot be probed, which callers read as "unknown" and treat as monochrome.
//
// This is the colour evidence for the formats that have no FITS header to carry BAYERPAT or NAXIS3:
// a SharpCap 16-bit mono TIFF and a DSLR-exported colour TIFF are the same extension and the same
// filename shape, and until this existed the pipeline could only tell them apart by guessing.
func stillChannels(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	cfg, _, derr := image.DecodeConfig(f)
	if derr != nil {
		return 0 // an unsupported flavour (e.g. a compression x/image/tiff lacks) — stay silent
	}
	return channelsFromColorModel(cfg.ColorModel)
}

// channelsFromColorModel maps a decoded image's colour model onto a plane count. Only the two grey
// models are monochrome; everything else — RGB, YCbCr, CMYK, paletted — carries colour, even when a
// particular file happens to be grey in content. We classify the CONTAINER, not the content: a
// three-channel file that looks grey still has to travel the colour path to come out unchanged.
func channelsFromColorModel(m color.Model) int {
	switch m {
	case color.GrayModel, color.Gray16Model:
		return 1
	default:
		return 3
	}
}
