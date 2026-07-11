package weathertile

import (
	"image"
	"math"

	"github.com/verove-jordan/astronomy/internal/weather"
)

const tileSize = 256

// bayer8 is an 8×8 ordered-dither threshold matrix (0..63): ±2/255 of spatially-ordered noise breaks the
// 8-bit alpha banding of large soft cloud fields without shifting the mean.
var bayer8 = [64]int{
	0, 32, 8, 40, 2, 34, 10, 42,
	48, 16, 56, 24, 50, 18, 58, 26,
	12, 44, 4, 36, 14, 46, 6, 38,
	60, 28, 52, 20, 62, 30, 54, 22,
	3, 35, 11, 43, 1, 33, 9, 41,
	51, 19, 59, 27, 49, 17, 57, 25,
	15, 47, 7, 39, 13, 45, 5, 37,
	63, 31, 55, 23, 61, 29, 53, 21,
}

func ditherAlpha(a float64, ox, oy int) uint8 {
	offset := ((float64(bayer8[(oy&7)*8+(ox&7)]) + 0.5) / 64 - 0.5) * 4
	v := math.Round(clamp01(a)*255 + offset)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// axisTaps precomputes, per output sample along one axis, the 4 edge-clamped source cell indices + their
// Catmull-Rom weights — so the two separable interpolation passes are pure multiply-adds.
type axisTaps struct {
	idx [][4]int
	w   [][4]float64
}

func tapsFromCoords(coords []float64, gridN int) axisTaps {
	out := axisTaps{idx: make([][4]int, len(coords)), w: make([][4]float64, len(coords))}
	for o, g := range coords {
		base := math.Floor(g)
		t := g - base
		t2 := t * t
		t3 := t2 * t
		out.w[o] = [4]float64{
			0.5 * (-t3 + 2*t2 - t),
			0.5 * (3*t3 - 5*t2 + 2),
			0.5 * (-3*t3 + 4*t2 + t),
			0.5 * (t3 - t2),
		}
		for j := 0; j < 4; j++ {
			s := int(base) - 1 + j
			if s < 0 {
				s = 0
			} else if s >= gridN {
				s = gridN - 1
			}
			out.idx[o][j] = s
		}
	}
	return out
}

// upsampleField resamples the nx×ny value grid to outW×outH with a separable 16-tap Catmull-Rom bicubic,
// clamped to the metric's [0,100] domain (Catmull-Rom overshoots at sharp fronts, which would colour-map
// into halos).
func upsampleField(cells []float32, nx, ny int, xt, yt axisTaps, outW, outH int) []float64 {
	mid := make([]float64, ny*outW) // horizontal pass
	for y := 0; y < ny; y++ {
		row := y * nx
		midRow := y * outW
		for ox := 0; ox < outW; ox++ {
			i := xt.idx[ox]
			wv := xt.w[ox]
			mid[midRow+ox] = float64(cells[row+i[0]])*wv[0] + float64(cells[row+i[1]])*wv[1] +
				float64(cells[row+i[2]])*wv[2] + float64(cells[row+i[3]])*wv[3]
		}
	}
	out := make([]float64, outW*outH) // vertical pass + domain clamp
	for oy := 0; oy < outH; oy++ {
		i := yt.idx[oy]
		wv := yt.w[oy]
		r0, r1, r2, r3 := i[0]*outW, i[1]*outW, i[2]*outW, i[3]*outW
		outRow := oy * outW
		for ox := 0; ox < outW; ox++ {
			v := mid[r0+ox]*wv[0] + mid[r1+ox]*wv[1] + mid[r2+ox]*wv[2] + mid[r3+ox]*wv[3]
			if v < 0 {
				v = 0
			} else if v > 100 {
				v = 100
			}
			out[outRow+ox] = v
		}
	}
	return out
}

// yNormToLat inverts Web-Mercator: a normalized world-Y (0 = north edge, 1 = south) → latitude in degrees,
// so each tile row samples the cube at its true latitude and the overlay coincides with the base map.
func yNormToLat(yn float64) float64 {
	n := math.Pi - 2*math.Pi*yn
	return (180 / math.Pi) * math.Atan(0.5*(math.Exp(n)-math.Exp(-n)))
}

// frameField returns the metric's cell values for the frame, or nil when absent/out of range.
func frameField(g weather.Grid, metric string, frame int) []float32 {
	frames := g.Layers[metric]
	if frame < 0 || frame >= len(frames) {
		return nil
	}
	return frames[frame]
}

// RenderTile paints one 256×256 weather tile for metric+frame of the cube at map tile (z,x,y): each pixel
// maps to its true lon/lat, then to a fractional grid coord, is bicubically sampled and colour-mapped, so
// the tile lines up on the ground like the base map's own tile. Returns (nil, false) when the tile is fully
// outside the cube hull or the metric/frame data is absent → the caller serves a transparent tile.
func RenderTile(g weather.Grid, metric string, frame, z, x, y int) (*image.NRGBA, bool) {
	if g.Nx < 1 || g.Ny < 1 {
		return nil, false
	}
	west, south, east, north := g.BBox[0], g.BBox[1], g.BBox[2], g.BBox[3]
	stepLon := 1.0
	if g.Nx > 1 {
		stepLon = (east - west) / float64(g.Nx-1)
	}
	stepLat := 1.0
	if g.Ny > 1 {
		stepLat = (north - south) / float64(g.Ny-1)
	}
	scale := math.Pow(2, float64(z))

	colGx := make([]float64, tileSize)
	colInside := make([]bool, tileSize)
	anyCol := false
	for ox := 0; ox < tileSize; ox++ {
		xn := (float64(x) + (float64(ox)+0.5)/tileSize) / scale
		lon := xn*360 - 180
		gx := 0.0
		if g.Nx > 1 {
			gx = (lon - west) / stepLon
		}
		colGx[ox] = gx
		if gx >= -0.001 && gx <= float64(g.Nx-1)+0.001 {
			colInside[ox] = true
			anyCol = true
		}
	}
	rowGy := make([]float64, tileSize)
	rowInside := make([]bool, tileSize)
	anyRow := false
	for oy := 0; oy < tileSize; oy++ {
		yn := (float64(y) + (float64(oy)+0.5)/tileSize) / scale
		lat := yNormToLat(yn)
		gy := 0.0
		if g.Ny > 1 {
			gy = (north - lat) / stepLat
		}
		rowGy[oy] = gy
		if gy >= -0.001 && gy <= float64(g.Ny-1)+0.001 {
			rowInside[oy] = true
			anyRow = true
		}
	}
	if !anyCol || !anyRow {
		return nil, false
	}

	xt := tapsFromCoords(colGx, g.Nx)
	yt := tapsFromCoords(rowGy, g.Ny)

	img := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	painted := false
	if metric == "clouds" {
		if fields := cloudBandFields(g, frame, xt, yt); fields != nil {
			paintBands(img, fields, colInside, rowInside)
			painted = true
		}
	}
	if !painted {
		if f := frameField(g, metric, frame); f != nil {
			if color := singleRamp(metric); color != nil {
				paintSingle(img, upsampleField(f, g.Nx, g.Ny, xt, yt, tileSize, tileSize), color, colInside, rowInside)
				painted = true
			}
		}
	}
	if !painted {
		return nil, false
	}
	return img, true
}

// cloudBandFields upsamples the three cloud altitude bands for the frame, or nil when the cube lacks any of
// them (→ the single-cover fallback).
func cloudBandFields(g weather.Grid, frame int, xt, yt axisTaps) [][]float64 {
	fields := make([][]float64, len(cloudBands))
	for i, bd := range cloudBands {
		f := frameField(g, bd.metric, frame)
		if f == nil {
			return nil
		}
		fields[i] = upsampleField(f, g.Nx, g.Ny, xt, yt, tileSize, tileSize)
	}
	return fields
}

// paintSingle colour-maps one upsampled field into the tile (the single-ramp path).
func paintSingle(img *image.NRGBA, field []float64, color func(float64) rgba, colInside, rowInside []bool) {
	for oy := 0; oy < tileSize; oy++ {
		rowIn := rowInside[oy]
		for ox := 0; ox < tileSize; ox++ {
			c := color(field[oy*tileSize+ox])
			a := uint8(0)
			if rowIn && colInside[ox] {
				a = ditherAlpha(c.a, ox, oy)
			}
			setPix(img, ox, oy, c.r, c.g, c.b, a)
		}
	}
}

// paintBands source-over composites the altitude bands per pixel (bands[0] at the bottom), accumulating
// premultiplied then dividing colour back out (NRGBA wants straight RGBA), matching the client renderer.
func paintBands(img *image.NRGBA, fields [][]float64, colInside, rowInside []bool) {
	for oy := 0; oy < tileSize; oy++ {
		rowIn := rowInside[oy]
		for ox := 0; ox < tileSize; ox++ {
			p := oy*tileSize + ox
			var r, g, b, a float64
			for n, bd := range cloudBands {
				c := bd.at(fields[n][p])
				sa := clamp01(c.a)
				inv := 1 - sa
				r = float64(c.r)*sa + r*inv
				g = float64(c.g)*sa + g*inv
				b = float64(c.b)*sa + b*inv
				a = sa + a*inv
			}
			var cr, cg, cb uint8
			if a > 0 {
				cr = uint8(math.Round(r / a))
				cg = uint8(math.Round(g / a))
				cb = uint8(math.Round(b / a))
			}
			out := uint8(0)
			if rowIn && colInside[ox] {
				out = ditherAlpha(a, ox, oy)
			}
			setPix(img, ox, oy, cr, cg, cb, out)
		}
	}
}

func setPix(img *image.NRGBA, x, y int, r, g, b, a uint8) {
	i := img.PixOffset(x, y)
	img.Pix[i] = r
	img.Pix[i+1] = g
	img.Pix[i+2] = b
	img.Pix[i+3] = a
}
