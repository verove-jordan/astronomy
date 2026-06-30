package lightpollution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// tileFetchAttempts is the number of upstream GETs per tile (2 retries). The handler surfaces a final
// failure as a 5xx so the map retries that tile rather than caching a blank — keep retries modest,
// since a Leaflet viewport fires ~10–20 tile requests at once and we must not amplify an upstream
// rate-limit.
const tileFetchAttempts = 3

// ErrNoTileSource means no overlay tile URL is configured; the caller should serve a transparent tile
// so the map simply shows no overlay rather than broken tiles.
var ErrNoTileSource = errors.New("lightpollution: no tile source configured")

// FetchTile returns a local path to the overlay tile at z/x/y, fetching it from the configured upstream
// (key injected server-side) and caching it on disk. Tiles are treated as static — a cached tile is
// reused indefinitely. On an upstream failure a stale cached tile is returned if present, else an error.
func (p *Provider) FetchTile(ctx context.Context, z, x, y int) (string, error) {
	if p.tileURL == "" {
		return "", ErrNoTileSource
	}
	cached := filepath.Join(p.cacheDir, "tiles", fmt.Sprintf("%d", z), fmt.Sprintf("%d", x), fmt.Sprintf("%d.png", y))
	if _, err := os.Stat(cached); err == nil {
		return cached, nil
	}
	body, err := p.fetchTileBytes(ctx, expandTileURL(p.tileURL, z, x, y, p.apiKey))
	if err != nil {
		return "", err
	}
	if err := fsutil.EnsureDir(filepath.Dir(cached)); err != nil {
		return "", err
	}
	tmp := cached + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, cached); err != nil {
		return "", err
	}
	return cached, nil
}

// fetchTileBytes GETs one upstream tile, retrying transient failures (transport error, 5xx, 429). A
// malformed URL or a 4xx is permanent and returns immediately.
func (p *Provider) fetchTileBytes(ctx context.Context, endpoint string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < tileFetchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err // malformed URL — not transient
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := p.http.Do(req)
		if err != nil {
			lastErr = err // transport error / timeout — retry
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("tile upstream status %d", resp.StatusCode)
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				continue // transient — retry
			}
			return nil, lastErr // 4xx — permanent
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if err != nil || len(body) == 0 {
			lastErr = fmt.Errorf("tile upstream read: %w", err)
			continue
		}
		return body, nil
	}
	return nil, lastErr
}
