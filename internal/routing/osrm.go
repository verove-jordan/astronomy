package routing

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"encoding/json"
)

const userAgent = "AstroStack/1.0 (+https://github.com/verove-jordan/astronomy)"

// osrmTable is the OSRM /table response. distances[i][j] is metres and durations[i][j] is seconds from
// source i to coordinate j; entries are null (nil pointer) for unroutable pairs.
type osrmTable struct {
	Code      string       `json:"code"`
	Distances [][]*float64 `json:"distances"`
	Durations [][]*float64 `json:"durations"`
}

// fetchTable asks the OSRM table service for the distance + duration from the source (coordinate 0) to each
// destination (coordinates 1…N) in one request. Coordinates are lon,lat per the OSRM/GeoJSON convention.
func (p *Provider) fetchTable(ctx context.Context, srcLat, srcLon float64, dstLats, dstLons []float64) ([]Drive, error) {
	var coords strings.Builder
	coords.WriteString(lonLat(srcLon, srcLat))
	for i := range dstLats {
		coords.WriteByte(';')
		coords.WriteString(lonLat(dstLons[i], dstLats[i]))
	}
	url := fmt.Sprintf("%s/table/v1/driving/%s?sources=0&annotations=distance,duration",
		strings.TrimRight(p.baseURL, "/"), coords.String())

	var r osrmTable
	if err := p.getJSON(ctx, url, &r); err != nil {
		return nil, err
	}
	if r.Code != "Ok" || len(r.Distances) == 0 || len(r.Durations) == 0 {
		return nil, fmt.Errorf("osrm table: code %q", r.Code)
	}
	dist, dur := r.Distances[0], r.Durations[0] // row for source 0; index 0 is source→source
	n := len(dstLats)
	if len(dist) < n+1 || len(dur) < n+1 {
		return nil, fmt.Errorf("osrm table: got %d/%d values for %d destinations", len(dist), len(dur), n)
	}
	out := make([]Drive, n)
	for i := 0; i < n; i++ {
		dm, ds := dist[i+1], dur[i+1] // +1 skips the source→source entry
		if dm == nil || ds == nil {
			continue // unroutable → OK stays false
		}
		out[i] = Drive{DistanceKm: *dm / 1000, DurationMin: *ds / 60, OK: true}
	}
	return out, nil
}

func (p *Provider) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("routing upstream status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func lonLat(lon, lat float64) string {
	return strconv.FormatFloat(lon, 'f', 6, 64) + "," + strconv.FormatFloat(lat, 'f', 6, 64)
}
