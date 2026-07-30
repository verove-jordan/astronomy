// moonvalid: planetary validation helpers — crop/stretch a FITS master to PNG, montage PNGs.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sort"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: moontools fitscrop <in.fits> <x> <y> <w> <h> <out.png> | montage <out.png> <in1.png> <in2.png> ...")
	}
	switch os.Args[1] {
	case "fitscrop":
		fitscrop(os.Args[2:])
	case "montage":
		montage(os.Args[2:])
	case "centroid":
		centroid(os.Args[2:])
	default:
		fail("unknown subcommand " + os.Args[1])
	}
}

// centroid prints the lit disc's brightness-weighted centre — the anchor for disc-relative
// crops across masters whose reference frames (and rasters) differ.
func centroid(args []string) {
	if len(args) != 1 {
		fail("centroid <in.fits>")
	}
	im, err := fits.ReadImage(args[0])
	if err != nil {
		fail(err.Error())
	}
	p := im.Pix[0]
	vals := make([]float64, 0, 100000)
	for i := 0; i < len(p); i += len(p)/100000 + 1 {
		vals = append(vals, float64(p[i]))
	}
	sort.Float64s(vals)
	bg, pk := vals[len(vals)/5], vals[int(0.999*float64(len(vals)-1))]
	thr := float32(bg + 0.25*(pk-bg))
	var sx, sy, s float64
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			if v := p[y*im.W+x]; v > thr {
				sx += float64(x) * float64(v)
				sy += float64(y) * float64(v)
				s += float64(v)
			}
		}
	}
	fmt.Printf("%.1f %.1f %d %d\n", sx/s, sy/s, im.W, im.H)
}

func fail(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }

func atoi(s string) int { v, err := strconv.Atoi(s); if err != nil { fail(err.Error()) }; return v }

// fitscrop renders a rectangle of a linear FITS master with a display stretch (P1→black,
// P99.9→white, gamma 0.45) so masters of different scales compare fairly.
func fitscrop(args []string) {
	if len(args) != 6 {
		fail("fitscrop <in.fits> <x> <y> <w> <h> <out.png>")
	}
	im, err := fits.ReadImage(args[0])
	if err != nil {
		fail(err.Error())
	}
	x0, y0, w, h := atoi(args[1]), atoi(args[2]), atoi(args[3]), atoi(args[4])
	p := im.Pix[0]
	vals := make([]float64, 0, 100000)
	for i := 0; i < len(p); i += len(p)/100000 + 1 {
		vals = append(vals, float64(p[i]))
	}
	sort.Float64s(vals)
	bg, pk := vals[len(vals)/100], vals[int(0.999*float64(len(vals)-1))]
	out := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := x0+x, y0+y
			if sx < 0 || sy < 0 || sx >= im.W || sy >= im.H {
				continue
			}
			v := (float64(p[sy*im.W+sx]) - bg) / math.Max(pk-bg, 1e-9)
			v = math.Pow(math.Min(math.Max(v, 0), 1), 0.45)
			out.SetGray(x, y, color.Gray{Y: uint8(v*254 + 0.5)})
		}
	}
	writePNG(args[5], out)
}

// montage lays PNGs out horizontally with a 12 px gap.
func montage(args []string) {
	if len(args) < 3 {
		fail("montage <out.png> <in1> <in2> ...")
	}
	var imgs []image.Image
	wSum, hMax := 0, 0
	for _, p := range args[1:] {
		f, err := os.Open(p)
		if err != nil {
			fail(err.Error())
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			fail(p + ": " + err.Error())
		}
		imgs = append(imgs, img)
		wSum += img.Bounds().Dx()
		if img.Bounds().Dy() > hMax {
			hMax = img.Bounds().Dy()
		}
	}
	const gap = 12
	canvas := image.NewRGBA(image.Rect(0, 0, wSum+gap*(len(imgs)-1), hMax))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{30, 30, 34, 255}), image.Point{}, draw.Src)
	x := 0
	for _, img := range imgs {
		r := image.Rect(x, 0, x+img.Bounds().Dx(), img.Bounds().Dy())
		draw.Draw(canvas, r, img, img.Bounds().Min, draw.Src)
		x += img.Bounds().Dx() + gap
	}
	writePNG(args[0], canvas)
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		fail(err.Error())
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fail(err.Error())
	}
}
