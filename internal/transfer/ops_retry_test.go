package transfer

import (
	"context"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

var errTransient = minio.ErrorResponse{StatusCode: 503, Code: "SlowDown"}

func TestRunUpload_RetriesMidFileWithoutDoubleCounting(t *testing.T) {
	fastFileRetry(t)
	req, _ := writeTestFolder(t)
	req.Op = OpUpload
	fake := newFakeS3()
	// First attempt on a.fits drops the connection after 3 of its 5 bytes were already reported.
	fake.uploadFails["acct/data/M101/a.fits"] = 1
	fake.failWith = errTransient
	fake.partial = 3

	var events []Progress
	res, err := runUpload(context.Background(), fake, req, false, func(p Progress) { events = append(events, p) })

	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
	assert.Equal(t, int64(8), res.Bytes, "5 + 3 bytes, counted once despite the retry")
	assert.Equal(t, 2, fake.uploadCalls["acct/data/M101/a.fits"], "failed once, then succeeded")
	assert.Equal(t, 1, fake.uploadCalls["acct/data/M101/lights/b.fits"])

	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, last.BytesTotal, last.BytesDone, "BytesDone == BytesTotal exactly after a mid-file retry")
	assert.Equal(t, int64(8), last.BytesTotal)
	for _, p := range events {
		assert.LessOrEqual(t, p.BytesDone, p.BytesTotal, "progress never overshoots the total")
	}
}

func TestRunUpload_ExhaustsRetriesThenFails(t *testing.T) {
	fastFileRetry(t)
	req, folder := writeTestFolder(t)
	req.Op = OpUpload
	fake := newFakeS3()
	fake.uploadFails["acct/data/M101/a.fits"] = 99 // never recovers
	fake.failWith = errTransient

	_, err := runUpload(context.Background(), fake, req, false, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, errTransient)
	assert.Equal(t, fileRetryAttempts, fake.uploadCalls["acct/data/M101/a.fits"], "bounded attempts")
	assert.FileExists(t, folder+"/a.fits", "the local file is untouched")
}

func TestRunUpload_NonRetryableFailsOnFirstAttempt(t *testing.T) {
	fastFileRetry(t)
	req, _ := writeTestFolder(t)
	req.Op = OpUpload
	fake := newFakeS3()
	fake.uploadFails["acct/data/M101/a.fits"] = 99
	fake.failWith = minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}

	_, err := runUpload(context.Background(), fake, req, false, nil)

	require.Error(t, err)
	assert.Equal(t, 1, fake.uploadCalls["acct/data/M101/a.fits"], "a real S3 answer is not retried")
}

func TestRunDownload_RetriesMidFileWithoutDoubleCounting(t *testing.T) {
	fastFileRetry(t)
	req, _ := writeTestFolder(t) // local copies exist but sizes are asserted via the fake's objects
	req.Op = OpDownload
	req.RelPath = "M42" // download into a folder that does not exist locally yet
	fake := newFakeS3()
	fake.objects["acct/data/M42/a.fits"] = objWithSize("acct/data/M42/a.fits", 5)
	fake.objects["acct/data/M42/lights/b.fits"] = objWithSize("acct/data/M42/lights/b.fits", 3)
	fake.downloadFails["acct/data/M42/a.fits"] = 1
	fake.failWith = errTransient
	fake.partial = 2

	var events []Progress
	res, err := runDownload(context.Background(), fake, req, func(p Progress) { events = append(events, p) })

	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
	assert.Equal(t, int64(8), res.Bytes)
	assert.Equal(t, 2, fake.downloadCalls["acct/data/M42/a.fits"])

	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, last.BytesTotal, last.BytesDone, "BytesDone == BytesTotal exactly after a mid-file retry")
	assert.Equal(t, int64(8), last.BytesTotal)
}

func TestRetryFile_CtxCancelStopsBetweenAttempts(t *testing.T) {
	// With the real (untouched) backoff, an already-cancelled ctx must interrupt the first sleep — the
	// attempt itself runs, but no retry is scheduled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0

	err := retryFile(ctx, func() error {
		calls++
		return errTransient
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls, "no second attempt after cancellation")
}

// objWithSize builds a fake listed object of the given size (mod time now, like a fresh upload).
func objWithSize(key string, size int64) s3store.Object {
	return s3store.Object{Key: key, Size: size, ModTime: time.Now().UnixMilli()}
}
