package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
)

// Retry policy for the NON-streaming S3 calls (stat, list, delete, small get/put, bucket checks):
// transient network and server-throttle failures are retried a few times with jittered exponential
// backoff; everything else (other 4xx, context cancellation, local file errors) fails immediately.
// Streaming Upload/Download are NOT wrapped here — internal/transfer retries those per file so it can
// reset its per-file byte counter and never double-count progress.
const (
	retryAttempts = 4
	retryCap      = 5 * time.Second
)

// retryBase is the first backoff step (doubled per attempt, capped at retryCap). A var so tests can
// shrink it; nothing may depend on exact sleep durations.
var retryBase = 300 * time.Millisecond

// withRetry runs fn up to retryAttempts times, sleeping a jittered exponential backoff between attempts.
// A nil or non-retryable error returns immediately (unwrapped, so callers can still inspect it, e.g.
// Stat's 404 check); exhaustion and a context cancellation during the sleep are wrapped with op.
func withRetry(ctx context.Context, op string, fn func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = fn()
		if err == nil || !IsRetryable(err) {
			return err
		}
		if attempt == retryAttempts {
			return fmt.Errorf("%s: giving up after %d attempts: %w", op, attempt, err)
		}
		if werr := sleepBackoff(ctx, attempt); werr != nil {
			return fmt.Errorf("%s: %w (last error: %v)", op, werr, err)
		}
	}
}

// sleepBackoff waits the backoff for the given 1-based attempt — base×2^(attempt-1) capped at retryCap,
// with full ±50% jitter so concurrent transfers don't retry in lockstep — honoring ctx cancellation.
// math/rand is fine here: the jitter only de-synchronizes retries, nothing depends on its values.
func sleepBackoff(ctx context.Context, attempt int) error {
	d := retryBase << (attempt - 1)
	if d > retryCap {
		d = retryCap
	}
	d = d/2 + time.Duration(rand.Int63n(int64(d)+1)) // uniform in [0.5d, 1.5d]
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsRetryable reports whether err is a transient S3/network failure worth retrying: timeouts, connection
// resets and broken pipes, truncated bodies, and throttling/server-side HTTP responses. Context errors
// and every other 4xx are never retryable. Exported for internal/transfer's per-file streaming retries.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Context errors first: context.DeadlineExceeded also implements net.Error with Timeout()==true,
	// but an aborted caller must never be retried.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		switch resp.Code {
		case "SlowDown", "RequestTimeout", "InternalError":
			return true
		}
		return false // any other S3 response (NoSuchKey, AccessDenied, …) is a real answer, not a blip
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}
