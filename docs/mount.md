# Mount — the Celestron hand-controller link

AstroStack drives a Celestron NexStar-protocol mount (AVX, CGEM, Evolution…) through the **hand
controller's serial link**. On a MacBook that means the hand controller's **mini-USB socket**, and
this document is about making that link survive an unattended night.

- Code: `internal/device/nexstar/`
- Diagnose: `just mount-doctor`
- Prove: `just mount-probe`
- Endure: `just mount-soak 8h`

## Wiring, and the one thing that catches everybody

The NexStar+ hand controller has a mini-USB socket on its base. Behind it is a **Prolific PL2303**
USB-to-serial bridge, and **macOS ships no driver for it**. A `/dev/cu.*` device only appears once
Prolific's *PL2303 Serial* system extension is installed **and approved** in System Settings →
Privacy & Security — and the current version **refuses the discontinued PL2303 HXA/XA/TA parts**.

Three more things people lose an evening to:

- **The hand controller is powered by the mount, not by USB.** With the mount switched off, nothing
  enumerates at all. Power the mount, let the hand controller finish booting past its splash screen,
  then plug the cable in.
- **Many mini-USB cables are charge-only.** No data lines, no device.
- **The cable goes to the hand controller's own socket**, not the mount's AUX port.

`just mount-doctor` tells these apart. It reads the USB bus with `ioreg`, matches the bridge against
a table of known chips, and compares that with the list of serial devices — so "there is a Prolific
chip on the bus but no serial device for it" is reported as a **driver** problem, with the fix,
instead of a mysterious absence.

| Verdict | What it means | What to do |
|---|---|---|
| `ok` | A bridge is on the bus and owns a port (and, with `PROBE=1`, a mount answered) | Nothing |
| `no_usb_device` | Nothing that could be a hand controller is on the bus | Power the mount; check the cable is a data cable and goes to the hand controller |
| `driver_missing` | The bridge is there, no serial device exists for it | Install *PL2303 Serial* from the Mac App Store, approve it in Privacy & Security, replug |
| `chip_unsupported` | As above, and the chip is a discontinued Prolific revision | Legacy Prolific driver, or use the RS-232 fallback below |
| `port_busy` | The port exists but another program holds it | Stop `just device`, CPWI, or a planetarium app still connected |
| `permission_denied` | The port exists but this user may not open it | Check the device permissions and any security tooling |
| `no_reply` | The port opens, nothing answers the echo | Mount off, hand controller still booting, or the wrong socket |

### The RS-232 fallback

Every Celestron hand controller also has an **RJ-11 serial port** on its base. With a Celestron
RS-232 cable and any **FTDI** USB-serial adapter, macOS needs no extra software at all — and the
protocol, the baud rate and this driver are identical. Only the `/dev` path differs. If the mini-USB
path is blocked by the chip revision, this is the way through, and it is worth owning the cable
before the first clear night rather than after it.

## The protocol, and why the driver looks the way it does

9600 8N1, no flow control, replies terminated by `#`. Celestron's own developer notes state the two
facts the whole design turns on:

> Software drivers should be prepared to wait up to 3.5 s for a hand control response. If serial
> commands are "blindly" sent without waiting for a response, then some commands may be dropped or
> the software driver could see responses that are for earlier commands.

The second half is what ends a night. Every reply is either fixed-length binary or a `#`-terminated
string from a small grammar, so a reply belonging to the *previous* command still parses: `L`
("slewing?") reading `e`'s coordinates simply answers "not slewing", forever, and nothing above
notices. Hence:

- **One command in flight**, behind a mutex — that is the protocol, not caution.
- **A flush and a randomised-byte handshake on connect.** A port that merely opens is not empty; a
  run killed mid-command leaves an answer in the kernel's queue. The old fixed `Kx` handshake would
  have accepted a stale `x#` and started the session one reply behind.
- **A timeout is always followed by a resynchronisation, never a bare retry.** The first reply may
  still be arriving, and would then be read as the answer to the retry.
- **`Ping` — an echo of a byte the mount has never been asked to echo — is the only command whose
  wrong answer is detectable.** It is the link's proof of synchronisation, not just of liveness.

## What is safe to ask twice

`internal/device/nexstar/retry.go` classifies every frame by what a **second copy of it would do to
a telescope**, and defaults to never:

- **Always retried** — cancel-GoTo `M`, and both forms of rate-0 slew. These are the STOP; a
  duplicate does nothing, a *dropped* one leaves the mount moving.
- **Retried after a resynchronisation** — pure reads (position, slewing, aligned, tracking, pier
  side, model, firmware, site, clock, and the motor-controller reads).
- **Never** — GoTo, Sync, set-tracking, set-site, set-clock, hibernate, any non-zero slew rate, PEC
  writes and index seeks. A retried GoTo can send a settled tube back across the sky; two Syncs at
  two positions corrupt the pointing model invisibly.

A write whose reply never arrives returns `ErrUnknownOutcome`, not "failed" — reporting a timed-out
GoTo as a failure is how someone aborts a slew that is already happening.

## Recovery

- **Reconnect.** A vanished descriptor is detected (not confused with a slow mount), the port is
  re-scanned — macOS hands a re-enumerated bridge back under a *different*
  `/dev/cu.usbserial-XXXX`, because that suffix is a USB location id — and the mount is asked to
  prove it is the same one (echo, firmware, model). Backoff runs outside the driver's mutex, so
  STOP never queues behind it. It retries forever: a cable knocked at 3am should be found again at
  3.01am.
- **Never silently resumed.** If the mount comes back reporting itself unaligned, that is a power
  cycle: GoTo refuses with `ErrAlignmentLost` ("re-align") rather than slewing. PEC playback is not
  re-enabled either — a curve replayed against an unknown index phase tracks *worse* than none.
- **Jog deadman.** Every path that starts an axis registers the frame that halts it. If nobody
  renews within four seconds, the driver sends it; if the link died mid-move, the stop is the first
  thing sent on the reconnected port, before anything else can borrow it. This replaces a real bug —
  `nudgeAxis` and `PulseGuide` used to report success without stopping the motor when the port had
  gone, and a motor does not stop because a USB cable was pulled.

## The bench protocol

Run these in order with the mount powered and the hand controller booted. Each gates the next.

| Stage | Command | Pass |
|---|---|---|
| **A** — can the Mac see it | `just mount-doctor` | verdict `ok`, a `/dev/cu.usbserial-*` node, the chip named |
| **B** — identity + echo storm | `just mount-probe` | model and firmware reported; 500 echoes with **zero** errors and **zero** resyncs; p99 < 500 ms |
| **C** — resynchronisation torture | `ASTRO_TEST_MOUNT_LIVE=1 go test ./internal/device/nexstar -run TestLiveHandController -v` | 20 deliberately abandoned replies, all recovered, `unrecovered = 0` |
| **D** — motion (watch it) | add `ASTRO_TEST_MOUNT_MOTION=1` | tracking toggles, ±10″ out-and-back, and an unrenewed jog stops itself |
| **E** — unplug drill | pull the cable mid-run, replug into a **different** socket | the link comes back by itself, on the new path, without a restart |
| **F** — the night | `just mount-soak 1h` then `just mount-soak 8h` | see the thresholds below |

`MOTION=nudge just mount-soak 8h` adds ±10″ out-and-back moves every ten minutes and an hourly
jog-and-deadman check. It never sends a GoTo and never makes a large slew.

The soak holds a `caffeinate` assertion for its whole run — a sleeping Mac drops the USB bus, and an
eight-hour run that ends at the lid closing is not evidence of anything.

### Soak thresholds

At 9600 8N1 one byte takes 1.04 ms, so a precise position query is about 20 ms of pure wire time and
a full state read about 32 ms. The limits follow from that:

| | Limit |
|---|---|
| Unrecovered commands | **0** — non-negotiable |
| Replies proven to belong to the wrong command | **0** |
| Median / 99th-percentile / slowest reply | 120 ms / 800 ms / under 3500 ms |
| Resynchronisations | ≤ 2 per hour, and **0 in the final hour** |
| Reconnects | 0, unless an unplug drill asked for them |
| Goroutine / heap growth | ≤ 2 / ≤ 8 MB after a collection |
| Coverage | at least 90 % of the polls the cadence implies |

The report is written as text and JSON (`-report`, or `just mount-soak` writes into `output/`), so a
night that ends badly is diagnosable at breakfast rather than being "it stopped".

## Notes

- Exactly one process may hold the port: macOS serial opens take `TIOCEXCL`. Stop `just device`
  before running `mount probe` or `mount soak`, and vice versa. The tools say so when they hit it.
- The engine is cgo-free, so the serial library's `enumerator` sub-package (IOKit) is unused. That is
  why a reconnect proves identity by *asking the mount*, rather than by USB serial number — which the
  Prolific bridge in a NexStar+ typically does not carry anyway.
- `internal/align` and the `/goto` page are **advisory**: they plan alignment stars for you to enter
  on the hand controller. They never command the mount. Neither does the camera polar alignment
  below — it measures, and you turn the bolts.

## Polar alignment with the camera

The `/goto` page tells you where Polaris *should* sit on a polar scope. That is open loop: it never
looks through the telescope, so it cannot say how far off you actually are. The **Polar alignment**
panel on `/capture` closes the loop.

**How it works.** The mount is bolted to the ground, so turning the telescope about its
right-ascension axis sweeps the optical axis around a circle centred on that axis. Plate-solve four
frames along the sweep, express them in a ground-fixed frame, and the circle's centre *is* the mount's
polar axis. Comparing it with the celestial pole gives the altitude and azimuth errors — exactly what
the two adjusting bolts control.

Nothing about this needs a pointing model, encoders, or a mount the software can drive. **You turn the
axis by hand between frames** — clutch, hand controller, whatever you have. The fit needs to know that
it moved, not how far.

### Two ways in

**Find the pole (one frame, ten seconds).** Set declination to its 90° index so the tube looks straight
down the right-ascension axis, point roughly north, and press **Find the pole**. One exposure, one
solve, and the panel marks the exact celestial pole on the image — plus Polaris, so you can tell which
way you are looking — and tells you how far the middle of the frame is from it. Drive the marker onto
the crosshairs and you are aligned.

That is exactly what a polar scope does, and it is worth about the same: **half a degree**, set by your
cone error and by how precisely the declination index is set. Neither is measured, so the answer is
labelled "one frame" and carries the assumption it rests on. Use it to get on the pole fast, in the
dark, without lying under the mount hunting for Polaris.

The marker alone is useful even if you never align this way: it turns "where on earth is the pole from
here" into something you read off a screen, and it works when Polaris is behind a tree, since it only
needs *some* stars to solve.

**Measure it properly (four frames).** The procedure below. Two orders of magnitude better, because it
replaces the assumption with a measurement.

**The procedure.**

1. Point anywhere with stars, ideally near the meridian at declination 0–40° and away from the zenith.
   Polaris does not need to be visible, which is the point: a tree in front of it costs nothing.
2. Press **Start measuring**. It takes a frame from the live view and solves it.
3. Turn the right-ascension axis about 20°, leave declination alone, press **Next**. Three times.
4. Read the correction: how much too high the axis is, and how far east or west, with the direction
   named rather than signed.
5. Press **Adjust**. A ring appears on the live image marking where the middle of the frame has to end
   up. Turn the altitude and azimuth bolts until the ring sits on the crosshairs.

**Turn it far enough.** The axis uncertainty falls as the *square* of the total rotation, because what
pins the circle's centre is the curvature of the arc, not its length. Measured on this implementation,
with one arcsecond of plate-solve noise and three frames:

| total arc | 20° | 25° | 30° | 45° | 60° | 90° |
| --- | --- | --- | --- | --- | --- | --- |
| RMS error | 1.27′ | 0.82′ | 0.57′ | 0.26′ | 0.15′ | 0.07′ |
| 95th percentile | 2.59′ | 1.66′ | 1.15′ | 0.52′ | 0.29′ | 0.14′ |

So the 30° that feels sufficient leaves a one-in-twenty chance of missing the arcminute this exists to
find. The panel asks for four frames twenty degrees apart — sixty in total. Adding *frames* over the
same arc buys about a fifth of what adding *arc* does, because the two ends carry nearly all the
curvature; the fourth frame is there to cross-check, not to average.

**Two numbers that are easy to confuse.** The azimuth bolt turns through more azimuth than the sky
error it removes, by 1/cos(latitude) — a factor of 1.4 at latitude 45°, 2 at 60°. The panel quotes the
*knob* angle for that reason. Turning by the sky error instead undershoots by cos(latitude) every time.

**What it corrects for.** Plate solves speak J2000 while sidereal time speaks the equinox of date, and
the pole has moved about nine arcminutes between them — twenty times the error being measured, so the
conversion is not optional. Atmospheric refraction is modelled too (on by default): the solver reports
where a star *is*, while the telescope is aimed at where it *appears*, and over a sixty-degree arc
ignoring that biases the answer by a couple of arcminutes.

**Realistic accuracy.** Sub-arcminute is comfortably reachable with a sixty-degree arc. Past that the
limit stops being the arithmetic and becomes the mount: bearing runout, tube flexure with orientation,
and the tripod settling are worth five to thirty arcseconds on consumer gear and no amount of averaging
removes them.

**Building on it without a sky.** Simulated frames cannot be plate-solved — the bundled catalogue is
far too sparse for Siril to match — so `ASTRO_SIM_SOLVER=1` plus the `sim` camera and mount drivers
runs the whole procedure at a desk. Dial an error in with
`POST /api/device/live/simulate {"polar_error_alt_arcmin": 25, "polar_error_az_arcmin": -14}` and the
measurement finds it. See `.env.example` for the caveats.

One thing to expect there: the simulated mount reads about **nine arcminutes out with nothing
injected**. That is not a bug in either half. The simulator holds J2000 coordinates throughout, so the
sweep its ideal mount traces is a circle about the J2000 pole — and today's pole is nine arcminutes
away from that, which is precisely the precession the measurement is built to account for. It is also
not unrealistic: it is what a mount aligned from a printed J2000 chart and never touched since would
do. Dialled-in errors compose with that baseline, so compare two measurements rather than one against
zero.

Also worth knowing when scripting against it: sweep by **jogging** the right-ascension axis, not by
sending GoTos. The simulated mount lands a deliberate arcminute off every target it is *sent* to, which
is realistic and is why plate-solve centring exists — but four independent arcminute offsets do not lie
on one circle, and the fit reads that scatter as a polar error several times larger than the one under
test. Jogging leaves declination untouched and adds no pointing error, exactly as turning the axis by
hand does.

**Where the code lives.** The geometry is `internal/polaralign` (pure, no I/O). The session — camera,
frame gating, solving — is `internal/capture/polar.go`, engine-side because solving needs Siril. The
device server contributes `POST /live/save`, which writes the live view's newest frame so the engine
measures the picture the user is watching rather than stealing the camera to take its own.
