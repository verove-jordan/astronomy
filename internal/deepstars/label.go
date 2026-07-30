package deepstars

import (
	"fmt"
	"strconv"
	"strings"
)

// greekByToken maps HYG's 3-letter Bayer tokens to greek letters. The generator hard-fails on any
// token outside this map (plus an optional "-N" component suffix), so it is provably complete for
// the embedded catalogue.
var greekByToken = map[string]string{
	"Alp": "α", "Bet": "β", "Gam": "γ", "Del": "δ", "Eps": "ε", "Zet": "ζ",
	"Eta": "η", "The": "θ", "Iot": "ι", "Kap": "κ", "Lam": "λ", "Mu": "μ",
	"Nu": "ν", "Xi": "ξ", "Omi": "ο", "Pi": "π", "Rho": "ρ", "Sig": "σ",
	"Tau": "τ", "Ups": "υ", "Phi": "φ", "Chi": "χ", "Psi": "ψ", "Ome": "ω",
}

var superscripts = [10]string{"", "¹", "²", "³", "⁴", "⁵", "⁶", "⁷", "⁸", "⁹"}

// bayerLabel renders the Bayer designation ("α Lyr", "θ² Ori"), or "" when absent/unmapped.
func (s Star) bayerLabel() string {
	if s.Bayer == "" || s.Con == "" {
		return ""
	}
	base, idx := s.Bayer, 0
	if i := strings.IndexByte(base, '-'); i >= 0 {
		if n, err := strconv.Atoi(base[i+1:]); err == nil {
			idx = n
		}
		base = base[:i]
	}
	greek, ok := greekByToken[base]
	if !ok {
		return ""
	}
	sup := ""
	if idx >= 1 && idx <= 9 {
		sup = superscripts[idx]
	}
	return greek + sup + " " + s.Con
}

// designations lists the star's names best-first: proper → Bayer → Flamsteed → HD.
func (s Star) designations() []string {
	var out []string
	if s.Proper != "" {
		out = append(out, s.Proper)
	}
	if b := s.bayerLabel(); b != "" {
		out = append(out, b)
	}
	if s.Flam > 0 && s.Con != "" {
		out = append(out, fmt.Sprintf("%d %s", s.Flam, s.Con))
	}
	if s.HD > 0 {
		out = append(out, "HD "+strconv.Itoa(s.HD))
	}
	return out
}

// Primary returns the display designation ("Vega" → "α² Cen" → "61 Cyg" → "HD 48915" → "").
func (s Star) Primary() string {
	d := s.designations()
	if len(d) == 0 {
		return ""
	}
	return d[0]
}

// Secondary returns the designation after Primary ("Vega" → "α Lyr"), or "" when there is none.
func (s Star) Secondary() string {
	d := s.designations()
	if len(d) < 2 {
		return ""
	}
	return d[1]
}
