package weather

// CachedForecast returns a forecast for lat/lon ONLY if one is already cached and fresh; it never
// contacts an upstream feed.
//
// It exists for the map's hover tooltip. siteKey rounds to 0.01° (~1.1 km), so answering a hover with
// Forecast would mint a brand-new upstream request every few pixels of pointer travel and drain the
// Open-Meteo budget in seconds. A tooltip is a glance, not a query: showing weather where we happen to
// know it and nothing where we do not is the honest trade, and it keeps hovering free.
func (p *Provider) CachedForecast(lat, lon float64) (SiteForecast, bool) {
	if p == nil {
		return SiteForecast{}, false
	}
	return p.cachedForecast(siteKey(lat, lon))
}
