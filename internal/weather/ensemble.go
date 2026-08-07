package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// A deterministic forecast four nights out states one outcome with no hint of how likely it is. An
// ensemble runs the same model dozens of times from slightly different starting states, so counting
// how many members agree the night is clear turns "60% cloud" into "11 of 40 runs say clear" — which
// is the difference between a plan and a guess when deciding whether to drive two hours.
//
// It is deliberately a per-search, single-location figure: the ensemble endpoint returns one series
// per member, so it is far too heavy to ask per candidate.

// Confidence is how strongly an ensemble agrees about one night at one place.
type Confidence struct {
	Model        string  `json:"model"`
	Members      int     `json:"members"`
	ClearMembers int     `json:"clear_members"`
	Agreement    float64 `json:"agreement"`      // 0..1, the share of members forecasting a clear night
	MeanCloudPct float64 `json:"mean_cloud_pct"` // mean over members and hours
	SpreadPct    float64 `json:"spread_pct"`     // standard deviation across members
}

const (
	// clearMemberCloudPct is the mean night cloud below which one member counts as forecasting a clear
	// night. It is looser than the hourly "good" threshold on purpose: a member that clouds over for an
	// hour still delivered a usable night.
	clearMemberCloudPct = 35.0
	confidenceVersion   = 1
)

// ensembleResponse decodes the hourly block loosely: the member series are named cloud_cover_member01,
// _member02, … and their count depends on the model, so they cannot be a fixed struct.
type ensembleResponse struct {
	Hourly map[string]json.RawMessage `json:"hourly"`
}

// NightConfidence returns the ensemble agreement for the night spanning [startMs,endMs] at one point,
// or nil when the feature is disabled, the night is past the horizon, or the feed is unavailable. A
// missing confidence is not a degradation worth warning about — the ranking never depended on it — so
// failures are logged, not surfaced.
func (p *Provider) NightConfidence(ctx context.Context, lat, lon float64, startMs, endMs int64) *Confidence {
	if p.ensembleURL == "" || endMs <= startMs || p.rateLimited() || p.beyondHorizon(endMs) {
		return nil
	}

	key := fmt.Sprintf("conf_v%d_%+.2f_%+.2f_%d_%d_%s", confidenceVersion, lat, lon, startMs/1000, endMs/1000, p.ensembleModel)
	if c, at, ok := readJSON[Confidence](p.confidencePath(key)); ok && time.Since(at) < p.ttl {
		return &c
	}

	resp, err := p.fetchEnsemble(ctx, lat, lon, startMs, endMs)
	if err != nil {
		if errors.Is(err, ErrRateLimited) {
			p.tripRateLimit()
		}
		log.Printf("weather: ensemble confidence unavailable: %v", err)
		return nil
	}
	c, ok := summarizeEnsemble(resp, p.ensembleModel)
	if !ok {
		return nil
	}
	writeJSON(p.confidencePath(key), c)
	return &c
}

func (p *Provider) fetchEnsemble(ctx context.Context, lat, lon float64, startMs, endMs int64) (ensembleResponse, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&hourly=cloud_cover&models=%s&start_hour=%s&end_hour=%s&timezone=UTC",
		p.ensembleURL, ftoa(lat), ftoa(lon), p.ensembleModel, omHourParam(startMs), omHourParam(endMs))
	var resp ensembleResponse
	if err := p.getJSON(ctx, url, &resp); err != nil {
		return ensembleResponse{}, err
	}
	return resp, nil
}

// summarizeEnsemble reduces the per-member cloud series to a single agreement figure.
func summarizeEnsemble(resp ensembleResponse, model string) (Confidence, bool) {
	means := memberMeanClouds(resp)
	if len(means) < 2 { // one series is a deterministic run, not an ensemble — it carries no confidence
		return Confidence{}, false
	}

	c := Confidence{Model: model, Members: len(means)}
	var sum float64
	for _, m := range means {
		sum += m
		if m <= clearMemberCloudPct {
			c.ClearMembers++
		}
	}
	c.MeanCloudPct = round1(sum / float64(len(means)))
	c.Agreement = round2(float64(c.ClearMembers) / float64(len(means)))

	var variance float64
	for _, m := range means {
		variance += (m - sum/float64(len(means))) * (m - sum/float64(len(means)))
	}
	c.SpreadPct = round1(math.Sqrt(variance / float64(len(means))))
	return c, true
}

// memberMeanClouds returns each member's mean cloud cover over the requested hours. Members are read in
// sorted key order so the result is stable regardless of JSON map iteration.
func memberMeanClouds(resp ensembleResponse) []float64 {
	keys := make([]string, 0, len(resp.Hourly))
	for k := range resp.Hourly {
		if k == "cloud_cover" || strings.HasPrefix(k, "cloud_cover_member") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var out []float64
	for _, k := range keys {
		var series []*float64
		if err := json.Unmarshal(resp.Hourly[k], &series); err != nil {
			continue
		}
		var sum float64
		var n int
		for _, v := range series {
			if v != nil {
				sum += *v
				n++
			}
		}
		if n > 0 {
			out = append(out, sum/float64(n))
		}
	}
	return out
}
