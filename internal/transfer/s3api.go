package transfer

import (
	"context"
	"math/rand"
	"time"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// s3API is the slice of *s3store.Client the transfer engine actually uses, as an interface so tests can
// inject a flaky fake. The exported Run keeps the concrete *s3store.Client (which satisfies it), so call
// sites — the job manager's transfer lane — don't change.
type s3API interface {
	List(ctx context.Context, bucket, prefix string) ([]s3store.Object, error)
	Stat(ctx context.Context, bucket, key string) (obj s3store.Object, ok bool, err error)
	Upload(ctx context.Context, bucket, key, localPath string, onBytes func(delta int64)) error
	Download(ctx context.Context, bucket, key, localPath string, onBytes func(delta int64)) error
	// Readiness reports whether an object can be read now (instant class, or an archived object whose
	// restore has completed) — used to pre-flight a download so archived-not-restored objects surface as
	// an ArchivedError instead of a mid-stream InvalidObjectState failure.
	Readiness(ctx context.Context, bucket, key string) (s3store.Readiness, error)
}

// fileRetryAttempts bounds the per-file retries of a streaming upload/download. Streaming ops retry HERE
// (not inside s3store) so the caller can reset its per-file byte counter at each attempt and progress is
// never double-counted across a mid-file retry.
const fileRetryAttempts = 3

// fileRetryBase is the first backoff step between per-file attempts (doubled per attempt, ±50% jitter —
// same policy family as s3store's non-streaming retries). A var so tests can shrink it.
var fileRetryBase = 300 * time.Millisecond

// retryFile runs one file's streaming transfer, retrying transient failures (s3store.IsRetryable). fn
// must be restartable: each attempt streams the file again from byte zero.
func retryFile(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = fn()
		if err == nil || !s3store.IsRetryable(err) || attempt == fileRetryAttempts {
			return err
		}
		d := fileRetryBase << (attempt - 1)
		d = d/2 + time.Duration(rand.Int63n(int64(d)+1)) // full ±50% jitter
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
