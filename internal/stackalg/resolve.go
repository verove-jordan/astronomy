package stackalg

import "fmt"

// Resolve expands every "auto" in o against the number of frames actually being stacked and pins
// each rejection parameter inside its own algorithm's usable range. The result is a fully
// determined Options the renderers can consume without further decisions.
//
// This is the function that keeps an un-configured run byte-identical to a pre-knob one:
// Resolve(DefaultLights(), n) reproduces exactly the historical count-adaptive clause.
func Resolve(o Options, frames int) Options {
	out := o
	if out.Combine == CombineAuto {
		out.Combine = CombineMean
	}
	if out.Reject == RejectAuto {
		out.Reject = AutoReject(frames)
		out.Low, out.High = 0, 0 // an auto choice always uses that algorithm's own defaults
	}
	info, ok := RejectOf(out.Reject)
	if !ok { // unknown algorithm: fall back to the historical default rather than emitting junk
		out.Reject = AutoReject(frames)
		out.Low, out.High = 0, 0
		info, _ = RejectOf(out.Reject)
	}
	if !info.HasParams {
		out.Low, out.High = 0, 0
		return out
	}
	out.Low = resolveParam(out.Low, info.Low)
	out.High = resolveParam(out.High, info.High)
	return out
}

// resolveParam substitutes the algorithm's default for an unset (0) value and pins anything else
// into the algorithm's own usable range.
func resolveParam(v float64, p RejectParam) float64 {
	if v <= 0 {
		return p.Default
	}
	if v < p.Min {
		return p.Min
	}
	if v > p.Max {
		return p.Max
	}
	return v
}

// EngineFor decides who runs the stack: an explicit choice is honoured, "auto" stays on Siril
// unless the requested combination or rejection is one only the Go combiner implements.
func EngineFor(o Options) Engine {
	if o.Engine == EngineSiril || o.Engine == EngineNative {
		return o.Engine
	}
	if c, ok := CombineOf(o.Combine); ok && !c.SupportedBy(EngineSiril) {
		return EngineNative
	}
	if o.Reject != RejectAuto {
		if r, ok := RejectOf(o.Reject); ok && !r.SupportedBy(EngineSiril) {
			return EngineNative
		}
	}
	if o.LocalNorm {
		return EngineNative // no Siril equivalent of local normalization
	}
	return EngineSiril
}

// Validate reports why o cannot be run, or nil. It is the whitelist behind the API: an unknown
// enum value is an ERROR (a silent fallback would misreport which algorithm produced the master),
// whereas an out-of-range number is clamped by Resolve, not rejected.
func Validate(o Options) error {
	switch o.Engine {
	case "", EngineAuto, EngineSiril, EngineNative:
	default:
		return fmt.Errorf("unknown stacking engine %q (want auto, siril or native)", o.Engine)
	}
	combine, ok := CombineOf(o.Combine)
	if !ok {
		return fmt.Errorf("unknown combination method %q", o.Combine)
	}
	if o.Reject != RejectAuto {
		reject, ok := RejectOf(o.Reject)
		if !ok {
			return fmt.Errorf("unknown rejection algorithm %q", o.Reject)
		}
		if !combine.Rejects && o.Reject != RejectNone {
			return fmt.Errorf("%s stacking takes no pixel rejection (drop %q or pick mean)", combine.ID, o.Reject)
		}
		if e := EngineFor(o); !reject.SupportedBy(e) {
			return fmt.Errorf("the %s engine does not implement %q rejection", e, o.Reject)
		}
	}
	if e := EngineFor(o); !combine.SupportedBy(e) {
		return fmt.Errorf("the %s engine does not implement %q stacking", e, combine.ID)
	}
	if !IsNorm(o.Norm) {
		return fmt.Errorf("unknown normalization %q", o.Norm)
	}
	if !IsWeight(o.Weight) {
		return fmt.Errorf("unknown frame weighting %q", o.Weight)
	}
	return nil
}

// Clamp pins the free-standing numeric fields into their advertised bounds. The rejection
// parameters are deliberately NOT clamped here — their usable range depends on the algorithm and
// is applied by Resolve, so the stored value stays exactly what the user asked for.
func Clamp(o Options) Options {
	o.Low, o.High = clampf(o.Low, 0, SigmaMax), clampf(o.High, 0, SigmaMax)
	o.TrimFrac = clampf(o.TrimFrac, 0, 0.45)
	o.Feather = clampi(o.Feather, 0, 512)
	if o.LocalNormDegree != 0 {
		o.LocalNormDegree = clampi(o.LocalNormDegree, 1, 4)
	}
	return o
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
