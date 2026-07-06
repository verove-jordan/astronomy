package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timeoutErr is a minimal net.Error with Timeout()==true (e.g. an i/o timeout on a slow endpoint).
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"context deadline (also a net timeout)", context.DeadlineExceeded, false},
		{"wrapped context canceled", fmt.Errorf("s3 stat x: %w", context.Canceled), false},
		{"net timeout", timeoutErr{}, true},
		{"net timeout inside OpError", &net.OpError{Op: "read", Err: timeoutErr{}}, true},
		{"ECONNRESET inside OpError", &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, true},
		{"wrapped EPIPE", fmt.Errorf("s3 put x: %w", syscall.EPIPE), true},
		{"unexpected EOF (truncated body)", io.ErrUnexpectedEOF, true},
		{"plain EOF", io.EOF, false},
		{"http 408", minio.ErrorResponse{StatusCode: 408, Code: "RequestTimeout"}, true},
		{"http 429", minio.ErrorResponse{StatusCode: 429}, true},
		{"http 500", minio.ErrorResponse{StatusCode: 500}, true},
		{"http 502", minio.ErrorResponse{StatusCode: 502}, true},
		{"http 503 SlowDown", minio.ErrorResponse{StatusCode: 503, Code: "SlowDown"}, true},
		{"http 504", minio.ErrorResponse{StatusCode: 504}, true},
		{"code SlowDown without status", minio.ErrorResponse{Code: "SlowDown"}, true},
		{"code InternalError without status", minio.ErrorResponse{Code: "InternalError"}, true},
		{"http 404 NoSuchKey", minio.ErrorResponse{StatusCode: 404, Code: "NoSuchKey"}, false},
		{"http 403 AccessDenied", minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}, false},
		{"http 400", minio.ErrorResponse{StatusCode: 400, Code: "InvalidRequest"}, false},
		{"wrapped 502", fmt.Errorf("s3 get x: %w", minio.ErrorResponse{StatusCode: 502}), true},
		{"plain local error", errors.New("open /x: no such file"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsRetryable(tt.err))
		})
	}
}

// fastBackoff shrinks the retry backoff for the duration of one test (nothing may depend on exact sleeps).
func fastBackoff(t *testing.T) {
	t.Helper()
	old := retryBase
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = old })
}

func TestWithRetry(t *testing.T) {
	transient := minio.ErrorResponse{StatusCode: 503, Code: "SlowDown"}
	fatal := minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}

	tests := []struct {
		name      string
		failures  int   // fn fails this many times before succeeding
		failWith  error // error returned by the failing calls
		wantCalls int
		wantErr   bool
	}{
		{"immediate success", 0, nil, 1, false},
		{"success after transient failures", 2, transient, 3, false},
		{"exhaustion after max attempts", 99, transient, retryAttempts, true},
		{"non-retryable stops at first attempt", 99, fatal, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fastBackoff(t)
			calls := 0
			err := withRetry(context.Background(), "op", func() error {
				calls++
				if calls <= tt.failures {
					return tt.failWith
				}
				return nil
			})
			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.failWith, "the last underlying error stays inspectable")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWithRetry_CtxCancelStopsPromptly(t *testing.T) {
	// A long backoff would sleep ≥ 2.5s between attempts; cancellation must cut it short.
	old := retryBase
	retryBase = retryCap
	t.Cleanup(func() { retryBase = old })

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- withRetry(ctx, "op", func() error {
			calls++
			return minio.ErrorResponse{StatusCode: 503}
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Less(t, time.Since(start), 2*time.Second, "cancel must interrupt the backoff sleep")
		assert.Equal(t, 1, calls, "no further attempt after cancellation")
	case <-time.After(3 * time.Second):
		t.Fatal("withRetry did not return promptly after ctx cancel")
	}
}

func TestMD5File(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.bin")
	require.NoError(t, os.WriteFile(p, []byte("aaaaa"), 0o644))

	sum, err := MD5File(p)
	require.NoError(t, err)
	assert.Equal(t, "594f803b380a41396ed63dca39503542", sum, "hex md5 of 'aaaaa'")

	_, err = MD5File(filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)
}
