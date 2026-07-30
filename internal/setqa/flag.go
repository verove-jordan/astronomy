package setqa

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// setStats condenses a set's frame probes into the signals the flagging rules consume.
type setStats struct {
	measured       bool
	raw            float64 // median per-frame severity M = max(borderσ, gradσ/2)
	affectedFrac   float64 // share of sampled frames with M above half the absolute floor
	borderSigma    float64 // median dominant-channel border excess
	borderPct      float64
	gradSigma      float64
	gradPct        float64
	worstBorder    string
	worstChannel   string
	preview        string // frame with the highest M — shows the artifact best
	imbalanceCh    string // color channel dominating the border excess, "" when balanced/mono
	imbalanceSigma float64
}

// flagSets turns per-set probes into reports: absolute floor first (works for a lone set), then
// the relative outlier rule against same-filter siblings, then supplementary reasons.
func flagSets(sets []inspect.Set, probes [][]FrameProbe, opts Options) []SetReport {
	stats := make([]setStats, len(sets))
	for i := range sets {
		stats[i] = aggregateSet(probes[i], opts)
	}
	totals := totalsByFilter(sets)
	reports := make([]SetReport, len(sets))
	for i, set := range sets {
		reports[i] = buildReport(set, stats[i], len(probes[i]), totals[set.Key.Filter], opts)
	}
	applyRelativeRule(sets, stats, reports, opts)
	for i := range reports {
		if reports[i].Flagged && stats[i].imbalanceCh != "" {
			reports[i].Reasons = append(reports[i].Reasons, Reason{
				Code: "channel_imbalance", Channel: stats[i].imbalanceCh, Sigma: stats[i].imbalanceSigma,
				Text: fmt.Sprintf("%s channel border excess +%.1fσ above the others",
					stats[i].imbalanceCh, stats[i].imbalanceSigma),
			})
		}
	}
	return reports
}

func aggregateSet(probes []FrameProbe, opts Options) setStats {
	if len(probes) < 2 {
		return setStats{} // <2 readable samples say nothing — Measured=false downstream
	}
	var ms, borders, borderPcts, grads, gradPcts []float64
	byChannel := map[string][]float64{}
	affected := 0
	worstM := math.Inf(-1)
	st := setStats{measured: true}
	for _, p := range probes {
		dom := dominantChannel(p)
		gradS, gradP := maxGradient(p)
		m := math.Max(dom.BorderSigma, gradS/2)
		ms = append(ms, m)
		borders = append(borders, dom.BorderSigma)
		borderPcts = append(borderPcts, dom.BorderPct)
		grads = append(grads, gradS)
		gradPcts = append(gradPcts, gradP)
		if m > opts.AbsBorderSigma/2 {
			affected++
		}
		if m > worstM {
			worstM, st.preview, st.worstBorder, st.worstChannel = m, p.Path, dom.WorstBorder, dom.Channel
		}
		for _, cp := range p.Channels {
			if cp.Channel != "" {
				byChannel[cp.Channel] = append(byChannel[cp.Channel], cp.BorderSigma)
			}
		}
	}
	st.raw = median64(ms)
	st.affectedFrac = float64(affected) / float64(len(ms))
	st.borderSigma = median64(borders)
	st.borderPct = median64(borderPcts)
	st.gradSigma = median64(grads)
	st.gradPct = median64(gradPcts)
	st.imbalanceCh, st.imbalanceSigma = channelImbalance(byChannel, opts)
	return st
}

func dominantChannel(p FrameProbe) ChannelProbe {
	if len(p.Channels) == 0 {
		return ChannelProbe{}
	}
	best := p.Channels[0]
	for _, cp := range p.Channels[1:] {
		if cp.BorderSigma > best.BorderSigma {
			best = cp
		}
	}
	return best
}

func maxGradient(p FrameProbe) (sigma, pct float64) {
	for _, cp := range p.Channels {
		if cp.GradSigma > sigma {
			sigma, pct = cp.GradSigma, cp.GradPct
		}
	}
	return sigma, pct
}

// channelImbalance reports the color channel whose median border excess stands RelSigma above
// both others — the OSC version of "the R set has a halo".
func channelImbalance(byChannel map[string][]float64, opts Options) (string, float64) {
	if len(byChannel) < 3 {
		return "", 0
	}
	worstCh, worst, second := "", math.Inf(-1), math.Inf(-1)
	for ch, vals := range byChannel {
		med := median64(vals)
		if med > worst {
			worst, second, worstCh = med, worst, ch
		} else if med > second {
			second = med
		}
	}
	if excess := worst - second; excess > opts.RelSigma {
		return worstCh, excess
	}
	return "", 0
}

func buildReport(set inspect.Set, st setStats, sampled int, tot filterTotal, opts Options) SetReport {
	rep := SetReport{
		ID:                 set.Key.ID(),
		Key:                set.Key,
		Count:              set.Count,
		TotalIntegrationMs: set.TotalIntegrationMs,
		Sampled:            sampled,
		Measured:           st.measured,
		AffectedFrac:       st.affectedFrac,
		PreviewFrame:       st.preview,
		Impact:             buildImpact(set, tot),
	}
	if !st.measured {
		return rep
	}
	rep.BorderSigma, rep.BorderPct = st.borderSigma, st.borderPct
	rep.GradSigma, rep.GradPct = st.gradSigma, st.gradPct
	rep.WorstBorder = st.worstBorder
	rep.StackedSigma = st.raw * math.Sqrt(float64(max(set.Count, 1)))
	rep.Score = 100 * rep.StackedSigma / (rep.StackedSigma + opts.StackSigma)
	reason := severityReason(st)
	switch {
	// Strong per-frame artifact: plainly visible on individual subs.
	case st.raw >= opts.AbsBorderSigma && reason.AmplitudePct >= opts.AbsBorderPct && st.affectedFrac >= 0.5:
		rep.Flagged = true
		rep.Reasons = append(rep.Reasons, reason)
	// Stack-visible artifact: subtle per frame (raw ≥ 0.5σ keeps it a real signal, not the noise
	// floor) but CONSISTENT — it integrates to rep.StackedSigma in the final while noise cancels,
	// which is how a ~1σ one-sided glow becomes a red halo after stacking + stretch.
	case opts.StackSigma > 0 && rep.StackedSigma >= opts.StackSigma &&
		st.raw >= 0.5 && reason.AmplitudePct >= opts.MinAmplitudePct:
		rep.Flagged = true
		rep.Reasons = append(rep.Reasons, Reason{
			Code: "stack_visible", Border: reason.Border, Channel: reason.Channel,
			AmplitudePct: reason.AmplitudePct, Sigma: rep.StackedSigma,
			Text: fmt.Sprintf("subtle but consistent %s (+%.1fσ per frame, %.1f%% of sky) — integrates to ~%.0fσ over %d frames",
				reasonNoun(reason), st.raw, reason.AmplitudePct, rep.StackedSigma, set.Count),
		})
	}
	return rep
}

func reasonNoun(r Reason) string {
	if r.Code == "border_glow" {
		return r.Border + " border glow"
	}
	return "background gradient"
}

// severityReason names whichever term dominates the set's severity: a one-sided border glow, or
// a strong fitted background plane.
func severityReason(st setStats) Reason {
	if st.borderSigma >= st.gradSigma/2 {
		return Reason{
			Code: "border_glow", Border: st.worstBorder, Channel: st.worstChannel,
			AmplitudePct: st.borderPct, Sigma: st.borderSigma,
			Text: fmt.Sprintf("%s border glow +%.1fσ (%.1f%% of sky)", st.worstBorder, st.borderSigma, st.borderPct),
		}
	}
	return Reason{
		Code: "strong_gradient", Channel: st.worstChannel,
		AmplitudePct: st.gradPct, Sigma: st.gradSigma,
		Text: fmt.Sprintf("strong background gradient %.1fσ (%.1f%% of sky)", st.gradSigma, st.gradPct),
	}
}

// applyRelativeRule flags sets that stand out against their same-filter siblings even below the
// absolute floor — the per-night grading idea applied to whole sets.
func applyRelativeRule(sets []inspect.Set, stats []setStats, reports []SetReport, opts Options) {
	byFilter := map[string][]int{}
	for i, s := range sets {
		if stats[i].measured {
			byFilter[s.Key.Filter] = append(byFilter[s.Key.Filter], i)
		}
	}
	for filter, idxs := range byFilter {
		raws := make([]float64, len(idxs))
		for j, i := range idxs {
			raws[j] = stats[i].raw
		}
		for j, i := range idxs {
			outlier, sigma, ratio := relativeOutlier(raws, j, opts)
			if !outlier {
				continue
			}
			reports[i].Flagged = true
			reports[i].Reasons = append(reports[i].Reasons, Reason{
				Code: "outlier_vs_siblings", Border: stats[i].worstBorder,
				AmplitudePct: stats[i].borderPct, Sigma: sigma,
				Text: fmt.Sprintf("background artifact %.1f× the sibling %s sets", ratio, filterLabel(filter)),
			})
		}
	}
}

// relativeOutlier: with ≥3 siblings a MAD rule (plus a 50% excess gate so a tight clean group
// can't flag itself); with exactly 2 a plain ratio. Both require half the absolute floor, so a
// tiny-but-relatively-worse gradient never flags.
func relativeOutlier(raws []float64, j int, opts Options) (bool, float64, float64) {
	raw := raws[j]
	if raw < opts.AbsBorderSigma/2 || len(raws) < 2 {
		return false, 0, 0
	}
	if len(raws) == 2 {
		base := math.Max(raws[1-j], 0.5)
		if raw > 2*base {
			return true, math.Min(raw-base, 99), raw / base
		}
		return false, 0, 0
	}
	med, mad := median64(raws), madSigma(raws)
	if raw > med+opts.RelSigma*mad && raw > med*(1+opts.MinRelExcess) {
		sigma := math.Min((raw-med)/math.Max(mad, 1e-9), 99)
		return true, sigma, raw / math.Max(med, 0.5)
	}
	return false, 0, 0
}

func filterLabel(filter string) string {
	if filter == "" {
		return "light"
	}
	return filter
}

type filterTotal struct {
	frames        int
	integrationMs int64
}

func totalsByFilter(sets []inspect.Set) map[string]filterTotal {
	totals := map[string]filterTotal{}
	for _, s := range sets {
		t := totals[s.Key.Filter]
		t.frames += s.Count
		t.integrationMs += s.TotalIntegrationMs
		totals[s.Key.Filter] = t
	}
	return totals
}

func buildImpact(set inspect.Set, tot filterTotal) Impact {
	imp := Impact{
		Filter:              set.Key.Filter,
		FilterFrames:        tot.frames,
		FilterIntegrationMs: tot.integrationMs,
		LostFrames:          set.Count,
		LostIntegrationMs:   set.TotalIntegrationMs,
		SNRFactor:           1,
	}
	if tot.integrationMs <= 0 {
		return imp
	}
	imp.LostIntegrationPct = 100 * float64(set.TotalIntegrationMs) / float64(tot.integrationMs)
	kept := tot.integrationMs - set.TotalIntegrationMs
	if kept <= 0 {
		imp.EmptiesFilter = true
		imp.SNRFactor = 0
		return imp
	}
	imp.SNRFactor = math.Sqrt(float64(kept) / float64(tot.integrationMs))
	return imp
}
