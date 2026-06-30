package lightpollution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const userAgent = "AstroStack/1.0 (light-pollution lookup)"

// apiResp is the shape we try to read from a calibrated point-query API. A provider may return any one
// of these fields; we prefer SQM, then Bortle, then a luminance/radiance value. A bare numeric body is
// also accepted and treated as a radiance reading.
type apiResp struct {
	SQM       *float64 `json:"sqm"`
	Bortle    *int     `json:"bortle"`
	Luminance *float64 `json:"mcd_m2"`
	Radiance  *float64 `json:"radiance"`
	Value     *float64 `json:"value"`
}

// queryAPI fetches the site brightness from the configured keyed API. It returns an error (so the
// caller falls through to the offline atlas/default) on any network, status, or parse failure. The API
// key travels only in the request URL the operator configured; it is never logged.
func (p *Provider) queryAPI(ctx context.Context, lat, lon float64) (float64, error) {
	endpoint := expandPointURL(p.apiURL, lat, lon, p.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("light-pollution API status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return 0, err
	}
	sqm, ok := parseAPIValue(body)
	if !ok {
		return 0, fmt.Errorf("light-pollution API returned an unparseable body")
	}
	return sqm, nil
}

// parseAPIValue extracts an SQM from a JSON object (preferring sqm → bortle → luminance → radiance) or
// from a bare numeric body (treated as radiance).
func parseAPIValue(body []byte) (float64, bool) {
	var r apiResp
	if err := json.Unmarshal(body, &r); err == nil {
		switch {
		case r.SQM != nil:
			return *r.SQM, true
		case r.Bortle != nil:
			return bortleToSQM(*r.Bortle), true
		case r.Luminance != nil:
			return luminanceToSQM(*r.Luminance), true
		case r.Radiance != nil:
			return radianceToSQM(*r.Radiance), true
		case r.Value != nil:
			return radianceToSQM(*r.Value), true
		}
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(string(body)), 64); err == nil {
		return radianceToSQM(f), true
	}
	return 0, false
}
