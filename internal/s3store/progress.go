package s3store

import "io"

// countReader wraps an io.Reader and reports the number of bytes passed through on each Read, so an upload
// (minio reads from it) or a download (io.Copy reads from it) can drive a byte-level progress bar.
type countReader struct {
	r       io.Reader
	onBytes func(delta int64) // optional
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.onBytes != nil {
		c.onBytes(int64(n))
	}
	return n, err
}
