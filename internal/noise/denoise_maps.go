package noise

// buildMaps produces the two per-pixel maps the denoiser needs: sigmaMap (the local noise sigma,
// upsampled from the clamped per-tile grid) and mask (the SNR protection weight in [0,1], where 1
// keeps the original coefficients). It returns ok=false when the noise floor is unusable.
func buildMaps(pix, cJ []float32, wcoef [][]float32, w, h int, o Options) (sigmaMap, mask []float32, ok bool) {
	sigGrid, gw, gh, sigmaG, _ := tileSigmaGrid(wcoef[0], w, h)
	if !(sigmaG > 0) || !isFinite(sigmaG) {
		return nil, nil, false
	}
	bgGrid := tileBgGrid(cJ, w, h, gw, gh)
	sigmaMap = upsampleGrid(sigGrid, gw, gh, w, h)
	c2 := smoothAtScale(cJ, wcoef, 2) // scale-2 smooth for the SNR estimate (coarsest if Scales<3)
	mask = make([]float32, w*h)
	fillMask(mask, c2, sigmaMap, bgGrid, gw, gh, w, h, o.ProtectSNRLo, o.ProtectSNRHi)
	return sigmaMap, mask, true
}

// tileBgGrid returns the per-tile background (median of the coarsest smooth cLast over each tile).
func tileBgGrid(cLast []float32, w, h, gw, gh int) []float64 {
	grid := make([]float64, gw*gh)
	var buf []float64
	for ty := 0; ty < gh; ty++ {
		for tx := 0; tx < gw; tx++ {
			buf = tileValues(cLast, w, h, tileSize, tx, ty, buf[:0])
			grid[ty*gw+tx] = median64(buf)
		}
	}
	return grid
}

// smoothAtScale reconstructs the à-trous smooth c_k = cJ + Σ_{j>=k} wcoef[j]. For k beyond the last
// detail plane it returns cJ (the coarsest available smooth), matching the "Scales<k" fallback.
func smoothAtScale(cJ []float32, wcoef [][]float32, k int) []float32 {
	out := make([]float32, len(cJ))
	copy(out, cJ)
	for j := k; j < len(wcoef); j++ {
		wj := wcoef[j]
		for i := range out {
			out[i] += wj[i]
		}
	}
	return out
}

// upsampleGrid bilinearly upsamples a gw×gh tile grid to a per-pixel w×h float32 plane.
func upsampleGrid(grid []float64, gw, gh, w, h int) []float32 {
	out := make([]float32, w*h)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			gy := gridCoord(y, tileSize)
			base := y * w
			for x := 0; x < w; x++ {
				out[base+x] = float32(bilinearSample(grid, gw, gh, gridCoord(x, tileSize), gy))
			}
		}
	})
	return out
}

// fillMask writes the protection weight M(x,y) = smoothstep(lo, hi, (c2 − bg)/sigma) into mask, with
// bg bilinearly sampled per pixel from the tile background grid.
func fillMask(mask, c2, sigmaMap []float32, bgGrid []float64, gw, gh, w, h int, lo, hi float64) {
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			gy := gridCoord(y, tileSize)
			base := y * w
			for x := 0; x < w; x++ {
				i := base + x
				sig := float64(sigmaMap[i])
				if sig <= 0 {
					mask[i] = 1 // cannot estimate SNR here; keep the original coefficients
					continue
				}
				bg := bilinearSample(bgGrid, gw, gh, gridCoord(x, tileSize), gy)
				mask[i] = float32(smoothstep(lo, hi, (float64(c2[i])-bg)/sig))
			}
		}
	})
}

// applyThresholds soft-thresholds each detail plane against the per-pixel noise, then blends the
// original coefficients back in by the protection mask: w” = M·w + (1−M)·softThreshold(w).
func applyThresholds(wcoef [][]float32, sigmaMap, mask []float32, o Options) {
	for j := range wcoef {
		kj := o.K[j]
		if kj <= 0 {
			continue // no thresholding at this scale (result would equal the input)
		}
		thresholdScale(wcoef[j], sigmaMap, mask, kj*o.Strength*scaleSigma(j))
	}
}

// thresholdScale applies one detail plane's SNR-protected soft threshold in place. base is the
// scale-and-strength factor; the per-pixel threshold is base·sigma(x). Coefficients are chunked into
// contiguous index bands (they are independent) and processed concurrently.
func thresholdScale(w, sigmaMap, mask []float32, base float64) {
	parallelRows(len(w), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			wv := float64(w[i])
			thr := softThreshold(wv, base*float64(sigmaMap[i]))
			m := float64(mask[i])
			w[i] = float32(m*wv + (1-m)*thr)
		}
	})
}
