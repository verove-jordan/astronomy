package fits

// CDDeterminant returns the determinant of the WCS CD matrix and whether a usable plate-solve was
// present. The SIGN of this determinant is the image PARITY (handedness): two frames whose signs differ
// are mirror images of one another and can never be aligned by rotation alone (star registration matches
// asterisms by chirality), so they will not co-register until one is flipped.
//
// Siril writes the solution as a PC matrix + CDELT rather than an explicit CD matrix, so when CD is absent
// we reconstruct det(CD) = CDELT1·CDELT2·det(PC). Reading CDELT alone is insufficient — the solver often
// folds the reflection into PC (det(PC) = −1) while leaving both CDELT positive.
func (h *Header) CDDeterminant() (float64, bool) {
	if det, ok := h.cdMatrixDet(); ok {
		return det, true
	}
	return h.pcCdeltDet()
}

// cdMatrixDet computes det(CD) from an explicit CD matrix when all four cards are present.
func (h *Header) cdMatrixDet() (float64, bool) {
	c11, ok1 := h.Float("CD1_1")
	c12, ok2 := h.Float("CD1_2")
	c21, ok3 := h.Float("CD2_1")
	c22, ok4 := h.Float("CD2_2")
	if !(ok1 && ok2 && ok3 && ok4) {
		return 0, false
	}
	return nonZero(c11*c22 - c12*c21)
}

// pcCdeltDet computes det(CD) from the PC matrix + CDELT scaling (Siril's representation). PC defaults to
// the identity matrix when absent, per the FITS WCS convention.
func (h *Header) pcCdeltDet() (float64, bool) {
	d1, ok1 := h.Float("CDELT1")
	d2, ok2 := h.Float("CDELT2")
	if !(ok1 && ok2) {
		return 0, false
	}
	p11 := h.floatOr("PC1_1", 1)
	p12 := h.floatOr("PC1_2", 0)
	p21 := h.floatOr("PC2_1", 0)
	p22 := h.floatOr("PC2_2", 1)
	return nonZero(d1 * d2 * (p11*p22 - p12*p21))
}

// floatOr returns the card's float value, or def when the card is absent.
func (h *Header) floatOr(key string, def float64) float64 {
	if v, ok := h.Float(key); ok {
		return v
	}
	return def
}

// nonZero reports a determinant as usable only when it is non-zero (a zero determinant is a degenerate
// WCS from which no parity can be read).
func nonZero(det float64) (float64, bool) {
	if det == 0 {
		return 0, false
	}
	return det, true
}
