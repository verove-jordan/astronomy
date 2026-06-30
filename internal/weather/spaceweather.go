package weather

import (
	"context"
	"strconv"
	"time"
)

// fetchKp pulls the planetary K-index from NOAA SWPC. The product is a JSON array of rows
// (["time_tag","Kp","a_running","station_count"]); row 0 is a header. Returns the most recent Kp as
// "now" and the maximum over the returned window, plus the latest observation time.
func (p *Provider) fetchKp(ctx context.Context) (now, max float64, issuedMs int64, err error) {
	var rows [][]string
	if err = p.getJSON(ctx, p.swpcURL, &rows); err != nil {
		return 0, 0, 0, err
	}
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue // header / short row
		}
		kp, e := strconv.ParseFloat(row[1], 64)
		if e != nil {
			continue
		}
		now = kp
		if kp > max {
			max = kp
		}
		if t, e2 := time.Parse("2006-01-02 15:04:05", row[0]); e2 == nil {
			issuedMs = t.UTC().UnixMilli()
		}
	}
	return now, max, issuedMs, nil
}
