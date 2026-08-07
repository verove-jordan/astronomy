package siril

import (
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// StackClause renders the options tail of a Siril `stack` command — everything between the
// sequence name and the trailing `-out=`: the combination method with its rejection, the input
// normalization, and the optional output/weighting/diagnostic flags.
//
// frames is the number of images actually being stacked; it only matters when the rejection is
// left on "auto" (see stackalg.AutoReject). With the defaults every call site has always used,
// the output is byte-for-byte what the engine emitted before these knobs existed — the goldens in
// scripts_test.go pin that.
//
// Grammar notes verified live against the host Siril 1.4.3 (`help stack`), each one a silent
// failure if got wrong:
//   - there is no bare `stack <seq> mean`; average stacking REQUIRES a rejection word and two
//     parameters, so a rejection-less mean renders as `rej none`;
//   - `-norm=additive` is accepted and then SILENTLY IGNORED — only add/addscale/mul/mulscale are
//     real, which is why the normalization comes from a closed enum;
//   - sum/min/max take no normalization, no weighting and no rejection at all.
func StackClause(o stackalg.Options, frames int) string {
	o = stackalg.Resolve(o, frames)
	combine, ok := stackalg.CombineOf(o.Combine)
	if !ok || combine.SirilToken == "" {
		combine, _ = stackalg.CombineOf(stackalg.CombineMean)
	}

	var parts []string
	parts = append(parts, rejectionClause(combine, o))
	if combine.Normalizes {
		parts = append(parts, normArg(o.Norm))
		if o.FastNorm {
			parts = append(parts, "-fastnorm")
		}
	}
	if o.OutputNorm && combine.Normalizes {
		parts = append(parts, "-output_norm")
	}
	if combine.Rejects {
		if o.Weight != stackalg.WeightNone {
			parts = append(parts, "-weight="+string(o.Weight))
		}
		if o.RejMaps {
			parts = append(parts, "-rejmaps")
		}
	}
	if o.Feather > 0 {
		parts = append(parts, "-feather="+strconv.Itoa(o.Feather))
	}
	return strings.Join(parts, " ")
}

// rejectionClause renders the method word plus, for average stacking, its rejection and the two
// parameters Siril demands.
func rejectionClause(combine stackalg.CombineInfo, o stackalg.Options) string {
	if !combine.Rejects {
		return combine.SirilToken
	}
	info, ok := stackalg.RejectOf(o.Reject)
	if !ok || info.SirilToken == "" {
		return combine.SirilToken + " none"
	}
	if !info.HasParams {
		return combine.SirilToken + " " + info.SirilToken
	}
	return combine.SirilToken + " " + info.SirilToken + " " + num(o.Low) + " " + num(o.High)
}

// normArg renders the input-normalization flag. An empty (auto) normalization is treated as
// additive-with-scaling, the deep-sky default every light stack has always used.
func normArg(n stackalg.Norm) string {
	switch n {
	case stackalg.NormNone:
		return "-nonorm"
	case stackalg.NormAuto:
		return "-norm=" + string(stackalg.NormAddScale)
	default:
		return "-norm=" + string(n)
	}
}

// num renders a rejection parameter the way the historical literals were written: 3 (not 3.0),
// 1.8, 0.05.
func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
