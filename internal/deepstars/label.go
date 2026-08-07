package deepstars

import (
	"fmt"
	"strconv"
	"strings"
)

// greekByToken maps HYG's 3-letter Bayer tokens to greek letters. The generator hard-fails on any
// token outside this map (plus an optional "-N" component suffix), so it is provably complete for
// the embedded catalogue. It is derived from greekTokens/greekLetters (format.go) so the runtime
// map and the index the binary catalogue stores can never drift apart.
var greekByToken = func() map[string]string {
	m := make(map[string]string, len(greekTokens))
	for i, tok := range greekTokens {
		m[tok] = greekLetters[i]
	}
	return m
}()

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

// designations lists the star's names best-first: proper → Bayer → Flamsteed → HD → HIP → Tycho-2
// → Gaia DR3.
//
// The last two tiers are what make the deep catalogue worth having. Below roughly magnitude 9 a
// star has no proper name, no Bayer letter and usually no HD number, so on the old embedded extract
// it fell off the end of this chain and rendered as nothing at all — an anonymous circle. Almost
// every ATHYG star carries a Tycho-2 designation, and the handful that do not carry a Gaia source
// id, so a field of eleventh-magnitude stars is now fully identifiable. A Tycho or Gaia id is not a
// name anyone recognises, but it is a real, resolvable catalogue entry: it can be pasted into
// SIMBAD or Vizier, which "unnamed" cannot.
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
	if s.HIP > 0 {
		out = append(out, "HIP "+strconv.Itoa(s.HIP))
	}
	if s.TYC != "" {
		out = append(out, "TYC "+s.TYC)
	}
	if s.Gaia > 0 {
		out = append(out, "Gaia DR3 "+strconv.FormatUint(s.Gaia, 10))
	}
	return out
}

// Primary returns the display designation ("Vega" → "α² Cen" → "61 Cyg" → "HD 48915" →
// "TYC 4669-731-1" → "").
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
