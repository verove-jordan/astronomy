// Package ser writes SER video files — the planetary imaging format.
//
// Planetary work stacks thousands of short frames, so the capture format matters more than it does
// for deep sky. SER is chosen over AVI for three concrete reasons: it stores 16-bit data natively
// (AVI would truncate an ASI1600's 12-bit ADC output to 8 bits through most codecs), it applies no
// compression at all (lossy compression destroys exactly the fine detail lucky imaging exists to
// recover), and it carries a per-frame timestamp trailer that survives into the stack.
//
// The format is deliberately trivial: a fixed 178-byte header, then raw frames back to back, then
// an optional trailer of one 8-byte timestamp per frame. Everything is little-endian.
//
// Reference: the SER v3 specification (Heiko Wilkens / Grischa Hahn).
package ser

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"
)

// HeaderSize is the fixed size of a SER header, in bytes.
const HeaderSize = 178

// ColorID identifies the pixel layout. Mono and the Bayer variants are the ones a ZWO camera
// produces; the RGB entries exist so a file written elsewhere can be described.
type ColorID uint32

const (
	ColorMono      ColorID = 0
	ColorBayerRGGB ColorID = 8
	ColorBayerGRBG ColorID = 9
	ColorBayerGBRG ColorID = 10
	ColorBayerBGGR ColorID = 11
	ColorRGB       ColorID = 100
	ColorBGR       ColorID = 101
)

// Options describe the stream being written. They cannot change once writing starts — SER has one
// header for the whole file, so a mid-stream change would silently corrupt every later frame.
type Options struct {
	Width, Height int
	// BitDepth is bits per pixel per plane: 8 or 16. Anything else is rejected rather than guessed,
	// because a wrong depth makes every frame unreadable in a way that looks like corrupt data.
	BitDepth  int
	ColorID   ColorID
	Observer  string // 40 bytes, space padded
	Camera    string // 40 bytes
	Telescope string // 40 bytes
	// UTCStart stamps the file. Zero means "when the writer was created".
	UTCStart time.Time
}

// Writer streams frames into a SER file.
type Writer struct {
	f          *os.File
	opts       Options
	frameBytes int
	frames     int
	timestamps []time.Time
	closed     bool
}

// Create opens path and writes a placeholder header. The frame count is only known at Close, so the
// header is rewritten then — which is why Close must be called for a readable file.
func Create(path string, opts Options) (*Writer, error) {
	if opts.Width <= 0 || opts.Height <= 0 {
		return nil, fmt.Errorf("ser: frame size must be positive, got %dx%d", opts.Width, opts.Height)
	}
	if opts.BitDepth != 8 && opts.BitDepth != 16 {
		return nil, fmt.Errorf("ser: bit depth must be 8 or 16, got %d", opts.BitDepth)
	}
	if opts.UTCStart.IsZero() {
		opts.UTCStart = time.Now().UTC()
	}

	planes := 1
	if opts.ColorID == ColorRGB || opts.ColorID == ColorBGR {
		planes = 3
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		f:          f,
		opts:       opts,
		frameBytes: opts.Width * opts.Height * planes * opts.BitDepth / 8,
	}
	if _, err := f.Write(w.header()); err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

// header renders the 178-byte header for the current frame count.
func (w *Writer) header() []byte {
	b := make([]byte, HeaderSize)
	copy(b[0:14], "LUCAM-RECORDER") // the required file signature
	// LuID is unused by every reader in practice; 0 is what other writers emit.
	binary.LittleEndian.PutUint32(b[14:18], 0)
	binary.LittleEndian.PutUint32(b[18:22], uint32(w.opts.ColorID))
	// LittleEndian field: counter-intuitively, 0 here means the PIXEL data is big-endian and 1 means
	// little-endian. Writing 1 to match the x86/ARM data we produce.
	binary.LittleEndian.PutUint32(b[22:26], 1)
	binary.LittleEndian.PutUint32(b[26:30], uint32(w.opts.Width))
	binary.LittleEndian.PutUint32(b[30:34], uint32(w.opts.Height))
	binary.LittleEndian.PutUint32(b[34:38], uint32(w.opts.BitDepth))
	binary.LittleEndian.PutUint32(b[38:42], uint32(w.frames))
	writeFixed(b[42:82], w.opts.Observer)
	writeFixed(b[82:122], w.opts.Camera)
	writeFixed(b[122:162], w.opts.Telescope)
	binary.LittleEndian.PutUint64(b[162:170], toSerTime(w.opts.UTCStart))
	binary.LittleEndian.PutUint64(b[170:178], toSerTime(w.opts.UTCStart))
	return b
}

// writeFixed copies s into a fixed-width, space-padded field, truncating if it does not fit.
func writeFixed(dst []byte, s string) {
	for i := range dst {
		dst[i] = ' '
	}
	copy(dst, s)
}

// WriteFrame16 appends one 16-bit frame. The slice must hold exactly width×height×planes samples.
func (w *Writer) WriteFrame16(pix []uint16, stamp time.Time) error {
	if w.closed {
		return fmt.Errorf("ser: writer is closed")
	}
	if w.opts.BitDepth != 16 {
		return fmt.Errorf("ser: file is %d-bit, cannot write 16-bit frames", w.opts.BitDepth)
	}
	if len(pix)*2 != w.frameBytes {
		return fmt.Errorf("ser: frame has %d samples, expected %d", len(pix), w.frameBytes/2)
	}
	// A byte view over the samples: on every platform this engine runs on the layout is already
	// little-endian, so the frame goes out with no per-pixel work — which matters at 100 fps.
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&pix[0])), len(pix)*2)
	return w.appendFrame(raw, stamp)
}

// WriteFrame8 appends one 8-bit frame.
func (w *Writer) WriteFrame8(pix []byte, stamp time.Time) error {
	if w.closed {
		return fmt.Errorf("ser: writer is closed")
	}
	if w.opts.BitDepth != 8 {
		return fmt.Errorf("ser: file is %d-bit, cannot write 8-bit frames", w.opts.BitDepth)
	}
	if len(pix) != w.frameBytes {
		return fmt.Errorf("ser: frame has %d bytes, expected %d", len(pix), w.frameBytes)
	}
	return w.appendFrame(pix, stamp)
}

func (w *Writer) appendFrame(raw []byte, stamp time.Time) error {
	if _, err := w.f.Write(raw); err != nil {
		return err
	}
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	w.timestamps = append(w.timestamps, stamp.UTC())
	w.frames++
	return nil
}

// Frames is how many frames have been written so far.
func (w *Writer) Frames() int { return w.frames }

// Close writes the timestamp trailer, rewrites the header with the final frame count, and closes the
// file. A file closed any other way has a frame count of zero and reads as empty.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	defer func() { _ = w.f.Close() }()

	// Trailer: one 8-byte timestamp per frame, in order. Readers use it to reject frames taken
	// during a wind gust, so it is worth the eight bytes.
	trailer := make([]byte, 8*len(w.timestamps))
	for i, t := range w.timestamps {
		binary.LittleEndian.PutUint64(trailer[i*8:], toSerTime(t))
	}
	if _, err := w.f.Write(trailer); err != nil {
		return err
	}

	// The frame count was unknown when the header went out, so rewrite it now.
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := w.f.Write(w.header()); err != nil {
		return err
	}
	return w.f.Sync()
}

// SER timestamps count 100-nanosecond ticks from 0001-01-01 UTC — the .NET DateTime convention the
// format inherited, not the Unix epoch.
//
// The arithmetic deliberately avoids time.Duration. A Duration is int64 NANOSECONDS, which tops out
// around 292 years, so `t.Sub(year1)` silently saturates for any real date and every timestamp in
// the file would be identical garbage. Going through Unix seconds keeps the whole range exact.
const (
	ticksPerSecond = 10_000_000
	// epochTicks is 0001-01-01 → 1970-01-01: 62135596800 seconds.
	epochTicks = 62135596800 * ticksPerSecond
)

func toSerTime(t time.Time) uint64 {
	u := t.UTC()
	ticks := epochTicks + u.Unix()*ticksPerSecond + int64(u.Nanosecond())/100
	if ticks < 0 {
		return 0
	}
	return uint64(ticks)
}

// FromSerTime is the inverse, for reading files back.
func FromSerTime(ticks uint64) time.Time {
	rel := int64(ticks) - epochTicks
	sec := rel / ticksPerSecond
	nsec := (rel % ticksPerSecond) * 100
	return time.Unix(sec, nsec).UTC()
}
