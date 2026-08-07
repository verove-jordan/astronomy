package nexstar

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Where the mount thinks it is and what time it thinks it is.
//
// These matter more than they look. The hand controller computes every alt-azimuth from its own site
// and clock, so a wrong site or a clock an hour out points the telescope at the wrong part of the sky
// even when everything else is perfect — and nothing in the image says why. The app already knows
// both, so it can simply tell the mount rather than making somebody retype them on a four-line
// keypad in the dark.
//
// The encoding is the fiddly part, and the DST convention is where drivers get it wrong. Celestron's
// own worked example settles it: 3:26 PM on 6 April 2005 in US Eastern is sent as hour 15, offset
// byte 251 (−5) and DST 1 — that is the LOCAL WALL CLOCK, the zone's STANDARD offset, and a separate
// daylight-saving flag. Read back the other way, UTC = local − offset − (dst ? 1h : 0), which gives
// 19:26 UTC. Getting this wrong is a one-hour error, and one hour of sky is fifteen degrees.

// Site is an observing location as the hand controller stores it: whole degrees, minutes and seconds.
type Site struct {
	LatDeg float64 `json:"lat_deg"`
	LonDeg float64 `json:"lon_deg"`
}

// Clock is what the hand controller believes about the time.
type Clock struct {
	// UTC is the instant, reconstructed from the mount's local time, offset and DST flag.
	UTC time.Time `json:"utc"`
	// OffsetHours is the zone's STANDARD offset, not the current one — the mount stores them apart.
	OffsetHours int  `json:"offset_hours"`
	DST         bool `json:"dst"`
}

// Site reads the mount's stored location.
func (m *Mount) Site(ctx context.Context) (Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return Site{}, device.ErrNotConnected
	}
	// Eight raw bytes then '#'. Read by LENGTH: a longitude of 35 degrees IS '#', and scanning for
	// the terminator would stop on it, hand back a short body and leave the rest in the buffer.
	body, err := m.rawBinaryLocked([]byte("w"), 8)
	if err != nil {
		return Site{}, fmt.Errorf("read the mount's site: %w", err)
	}
	return decodeSite(body)
}

// SetSite tells the mount where it is, then reads it back and checks.
//
// The read-back is not belt and braces: the values are quantised to whole arcseconds on the way in,
// so the only way to know what the mount actually stored is to ask it.
func (m *Mount) SetSite(ctx context.Context, s Site) (Site, error) {
	if math.Abs(s.LatDeg) > 90 || math.Abs(s.LonDeg) > 180 {
		return Site{}, fmt.Errorf("site %.4f,%.4f is not on Earth", s.LatDeg, s.LonDeg)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return Site{}, device.ErrNotConnected
	}
	if _, err := m.rawLocked(encodeSite(s)); err != nil {
		return Site{}, fmt.Errorf("set the mount's site: %w", err)
	}
	body, err := m.rawBinaryLocked([]byte("w"), 8)
	if err != nil {
		return Site{}, fmt.Errorf("read back the mount's site: %w", err)
	}
	got, err := decodeSite(body)
	if err != nil {
		return Site{}, err
	}
	// One arcsecond of tolerance: that is the resolution the protocol has, so anything inside it is
	// agreement rather than error.
	if math.Abs(got.LatDeg-s.LatDeg) > 1.0/3600 || math.Abs(got.LonDeg-s.LonDeg) > 1.0/3600 {
		return got, fmt.Errorf("the mount stored %.5f,%.5f rather than %.5f,%.5f",
			got.LatDeg, got.LonDeg, s.LatDeg, s.LonDeg)
	}
	return got, nil
}

// Clock reads what the mount believes the time is.
func (m *Mount) Clock(ctx context.Context) (Clock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return Clock{}, device.ErrNotConnected
	}
	body, err := m.rawBinaryLocked([]byte("h"), 8)
	if err != nil {
		return Clock{}, fmt.Errorf("read the mount's clock: %w", err)
	}
	return decodeClock(body)
}

// SetClock sets the mount's clock from an instant and the zone it should be expressed in, then reads
// it back and checks it lands within a couple of seconds.
func (m *Mount) SetClock(ctx context.Context, t time.Time) (Clock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return Clock{}, device.ErrNotConnected
	}
	if _, err := m.rawLocked(encodeClock(t)); err != nil {
		return Clock{}, fmt.Errorf("set the mount's clock: %w", err)
	}
	body, err := m.rawBinaryLocked([]byte("h"), 8)
	if err != nil {
		return Clock{}, fmt.Errorf("read back the mount's clock: %w", err)
	}
	got, err := decodeClock(body)
	if err != nil {
		return Clock{}, err
	}
	// Three seconds of slack: the write, the read-back and the mount's own second boundary all land
	// in between, and a stricter check would fail on a link that is working perfectly.
	if d := got.UTC.Sub(t.UTC()); d > 3*time.Second || d < -3*time.Second {
		return got, fmt.Errorf("the mount's clock reads %s, %s away from what was sent",
			got.UTC.Format(time.RFC3339), d.Round(time.Second))
	}
	return got, nil
}

// Hibernate parks the mount's alignment so it survives a power cycle; Wake brings it back.
//
// This is the end-of-night command for a permanently mounted rig: the alignment is kept, so the next
// session starts pointed rather than starting over. It is deliberately not called from anywhere
// automatically — hibernating a mount somebody is about to move by hand loses them the alignment
// they think they still have.
func (m *Mount) Hibernate(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	_, err := m.rawLocked([]byte("x"))
	return err
}

func (m *Mount) Wake(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	_, err := m.rawLocked([]byte("y"))
	return err
}

// --- wire format ---------------------------------------------------------------------------------

// encodeSite builds the `W` frame: degrees, minutes and seconds as separate bytes, each with a
// separate sign flag (0 = north / east, 1 = south / west) rather than a signed value.
func encodeSite(s Site) []byte {
	latD, latM, latS, latNeg := degToDMS(s.LatDeg)
	lonD, lonM, lonS, lonNeg := degToDMS(s.LonDeg)
	return []byte{'W',
		latD, latM, latS, boolByte(latNeg),
		lonD, lonM, lonS, boolByte(lonNeg),
	}
}

func decodeSite(b []byte) (Site, error) {
	if len(b) < 8 {
		return Site{}, fmt.Errorf("malformed site reply %v", b)
	}
	return Site{
		LatDeg: dmsToDeg(b[0], b[1], b[2], b[3] != 0),
		LonDeg: dmsToDeg(b[4], b[5], b[6], b[7] != 0),
	}, nil
}

// encodeClock builds the `H` frame. See the file comment for why the offset is the STANDARD one and
// the DST flag is separate.
func encodeClock(t time.Time) []byte {
	loc := t.Location()
	std := standardOffsetHours(t)
	_, nowOffset := t.Zone()
	dst := nowOffset != std*3600

	local := t.In(loc)
	return []byte{'H',
		byte(local.Hour()), byte(local.Minute()), byte(local.Second()),
		byte(local.Month()), byte(local.Day()), byte(local.Year() % 100),
		offsetByte(std), boolByte(dst),
	}
}

func decodeClock(b []byte) (Clock, error) {
	if len(b) < 8 {
		return Clock{}, fmt.Errorf("malformed clock reply %v", b)
	}
	offset := int(int8(b[6]))
	dst := b[7] != 0

	// The mount holds a local wall clock; the instant is what everything else in this app works in.
	wall := time.Date(2000+int(b[5]), time.Month(b[3]), int(b[4]),
		int(b[0]), int(b[1]), int(b[2]), 0, time.UTC)
	utc := wall.Add(-time.Duration(offset) * time.Hour)
	if dst {
		utc = utc.Add(-time.Hour)
	}
	return Clock{UTC: utc, OffsetHours: offset, DST: dst}, nil
}

// standardOffsetHours is the zone's offset with daylight saving OFF.
//
// It is derived by asking the zone what it does in January and in July and taking the smaller — not
// by assuming daylight saving is exactly one hour, which is false in a handful of zones and would
// silently mis-set the clock there. Whole hours only: the protocol has no minutes field, so the
// half-hour zones lose their thirty minutes here and the mount's own pointing model absorbs it.
func standardOffsetHours(t time.Time) int {
	loc := t.Location()
	_, jan := time.Date(t.Year(), time.January, 15, 12, 0, 0, 0, loc).Zone()
	_, jul := time.Date(t.Year(), time.July, 15, 12, 0, 0, 0, loc).Zone()
	std := jan
	if jul < jan {
		std = jul
	}
	return std / 3600
}

// offsetByte encodes a signed hour offset the way the protocol wants it: negative zones as 256−n.
func offsetByte(hours int) byte { return byte(int8(hours)) }

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func degToDMS(deg float64) (d, m, s byte, negative bool) {
	negative = deg < 0
	deg = math.Abs(deg)
	total := int(math.Round(deg * 3600))
	return byte(total / 3600), byte((total % 3600) / 60), byte(total % 60), negative
}

func dmsToDeg(d, m, s byte, negative bool) float64 {
	deg := float64(d) + float64(m)/60 + float64(s)/3600
	if negative {
		return -deg
	}
	return deg
}
