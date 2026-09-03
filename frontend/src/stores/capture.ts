import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { apiGet, apiPost } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import { fetchPreviewBuffer } from "@/utils/previewbuf";
import type {
  CaptureProgress,
  CaptureSequence,
  CaptureSessionRow,
  DeviceCameraState,
  DeviceInfo,
  DeviceMountState,
  MountDiagnosis,
  DeviceStatus,
  DeviceWheelState,
  LiveStats,
  MountAudit,
  MountRestoreResult,
  PreviewImage,
} from "@/types";

// Camera, filter wheel, mount and the auto-run sequencer, as the browser sees them.
//
// Two different transports are in play and each is chosen for a reason: the live IMAGE is a binary
// 16-bit buffer polled on demand (the browser stretches it locally, so the sliders never wait for
// the network), while the live STATISTICS arrive over SSE (they are small, and the histogram should
// update the instant a frame lands). Sequencer progress rides its own SSE stream from the engine,
// which owns the session.

const LIVE_POLL_MS = 250;

export const useCaptureStore = defineStore("capture", () => {
  // --- device state ---------------------------------------------------------------------------
  const deviceStatus = ref<DeviceStatus | null>(null);
  // What the drivers can actually SEE right now, as opposed to which drivers are compiled in: the
  // simulator always offers one of each, and real hardware is appended only when a driver found it.
  // That distinction is what lets the UI default to the mount that is plugged in rather than to the
  // simulator.
  const deviceList = ref<DeviceInfo[]>([]);
  const camera = ref<DeviceCameraState | null>(null);
  const wheel = ref<DeviceWheelState | null>(null);
  const mount = ref<DeviceMountState | null>(null);
  const error = ref("");

  // --- live view ------------------------------------------------------------------------------
  const liveRunning = ref(false);
  const liveImage = ref<PreviewImage | null>(null);
  const liveStats = ref<LiveStats | null>(null);
  // When the exposure currently in flight is expected to land (ISO), and how long it is. Held apart
  // from liveStats because it describes the frame being TAKEN, not the last one measured.
  const liveExposureEnds = ref<string | null>(null);
  const liveExposureUs = ref<number>(0);
  const liveError = ref("");
  let liveTimer: number | undefined;
  let liveAbort: AbortController | null = null;
  let statsSource: EventSource | null = null;

  // --- sequencer ------------------------------------------------------------------------------
  const progress = ref<CaptureProgress | null>(null);
  const sessions = ref<CaptureSessionRow[]>([]);
  const sequences = ref<
    { id: number; name: string; payload: CaptureSequence }[]
  >([]);
  let progressSource: EventSource | null = null;

  const connected = computed(() => ({
    camera: !!camera.value?.connected,
    wheel: !!wheel.value?.connected,
    mount: !!mount.value?.connected,
  }));

  const running = computed(
    () =>
      progress.value?.status === "running" ||
      progress.value?.status === "paused",
  );

  async function refreshDevices(): Promise<void> {
    try {
      deviceStatus.value = await apiGet<DeviceStatus>("/api/device/status");
      error.value = "";
      if (!deviceStatus.value.running) {
        camera.value = null;
        wheel.value = null;
        mount.value = null;
        deviceList.value = [];
        return;
      }
      await Promise.all([
        refreshCamera(),
        refreshWheel(),
        refreshMount(),
        refreshDeviceList(),
      ]);
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
    }
  }

  // Discovery is best-effort: it only enriches the connect defaults, so a failure must not blank the
  // panel that reports whether the device server is up at all.
  async function refreshDeviceList(): Promise<void> {
    try {
      const res = await apiGet<{ devices: DeviceInfo[] }>(
        "/api/device/devices",
      );
      deviceList.value = res.devices ?? [];
    } catch {
      deviceList.value = [];
    }
  }

  async function refreshCamera(): Promise<void> {
    camera.value = await apiGet<DeviceCameraState>("/api/device/camera");
  }
  async function refreshWheel(): Promise<void> {
    wheel.value = await apiGet<DeviceWheelState>("/api/device/wheel");
  }
  async function refreshMount(): Promise<void> {
    mount.value = await apiGet<DeviceMountState>("/api/device/mount");
  }

  async function connectCamera(driver = "sim"): Promise<void> {
    camera.value = await apiPost<DeviceCameraState>(
      "/api/device/camera/connect",
      { driver },
    );
  }
  async function connectWheel(driver = "sim", names?: string[]): Promise<void> {
    wheel.value = await apiPost<DeviceWheelState>("/api/device/wheel/connect", {
      driver,
      names,
    });
  }
  async function connectMount(driver = "sim", port?: string): Promise<void> {
    mount.value = await apiPost<DeviceMountState>("/api/device/mount/connect", {
      driver,
      port,
    });
  }
  async function disconnect(kind: "camera" | "wheel" | "mount"): Promise<void> {
    await apiPost(`/api/device/${kind}/disconnect`, {});
    if (kind === "camera") camera.value = null;
    if (kind === "wheel") wheel.value = null;
    if (kind === "mount") mount.value = null;
  }

  // setControl writes one camera control and refreshes the snapshot, so the UI always reflects what
  // the hardware ACCEPTED rather than what was asked for (drivers clamp).
  async function setControl(name: string, value: number): Promise<void> {
    camera.value = await apiPost<DeviceCameraState>(
      "/api/device/camera/control",
      {
        name,
        value,
      },
    );
  }

  function controlValue(name: string): number | undefined {
    return camera.value?.controls?.find((c) => c.name === name)?.value;
  }

  async function setFilter(slot: number): Promise<void> {
    wheel.value = await apiPost<DeviceWheelState>(
      "/api/device/wheel/position",
      {
        slot,
        wait: true,
      },
    );
  }

  // --- live view ------------------------------------------------------------------------------

  async function startLive(intervalMs = 100): Promise<void> {
    await apiPost("/api/device/live/start", { interval_ms: intervalMs });
    liveRunning.value = true;
    pollLive();
    openStats();
  }

  async function stopLive(): Promise<void> {
    liveRunning.value = false;
    window.clearTimeout(liveTimer);
    liveAbort?.abort();
    closeStats();
    try {
      await apiPost("/api/device/live/stop", {});
    } catch {
      // stopping a stopped loop is not an error worth surfacing
    }
  }

  // pollLive fetches the newest frame on a timer rather than streaming it: a still image the user
  // is staring at does not need a socket, and polling recovers by itself from a device restart.
  function pollLive(maxEdge = 1024) {
    window.clearTimeout(liveTimer);
    if (!liveRunning.value) return;
    liveAbort?.abort();
    const ctl = new AbortController();
    liveAbort = ctl;
    void (async () => {
      try {
        liveImage.value = await fetchPreviewBuffer(
          `/api/device/live/frame?max=${maxEdge}`,
          ctl.signal,
        );
        liveError.value = "";
      } catch (e) {
        if (!ctl.signal.aborted) {
          liveError.value = e instanceof Error ? e.message : String(e);
        }
      } finally {
        if (liveRunning.value) {
          liveTimer = window.setTimeout(() => pollLive(maxEdge), LIVE_POLL_MS);
        }
      }
    })();
  }

  // fetchCrop asks for a full-resolution rectangle — the 1:1 zoom, showing real sensor pixels
  // instead of an upscaled preview.
  // maxEdge bounds the returned resolution. It matters: a near-full-frame crop at 4096 is ~25 MB of
  // 16-bit pixels per poll, and no display can show more than its own pixel count anyway. Callers pass
  // the width they can actually draw, so the transfer scales with the screen rather than the sensor.
  async function fetchCrop(
    x: number,
    y: number,
    w: number,
    h: number,
    maxEdge = 4096,
  ): Promise<PreviewImage> {
    const max = Math.max(64, Math.min(4096, Math.round(maxEdge)));
    return fetchPreviewBuffer(
      `/api/device/live/frame?x=${Math.round(x)}&y=${Math.round(y)}&w=${Math.round(w)}&h=${Math.round(h)}&max=${max}`,
    );
  }

  function openStats() {
    closeStats();
    statsSource = new EventSource("/api/device/live/events");
    statsSource.onmessage = (ev) => {
      try {
        const payload = JSON.parse(ev.data) as {
          stats?: LiveStats;
          exposure_ends?: string | null;
          exposure_us?: number;
        };
        if (payload.stats) liveStats.value = payload.stats;
        liveExposureEnds.value = payload.exposure_ends ?? null;
        if (typeof payload.exposure_us === "number") {
          liveExposureUs.value = payload.exposure_us;
        }
      } catch {
        // a malformed frame is not worth tearing the stream down for
      }
    };
    statsSource.onerror = () => {
      // EventSource reconnects on its own; surface nothing unless the user acts.
    };
  }

  function closeStats() {
    statsSource?.close();
    statsSource = null;
  }

  // simulate drives the simulated observatory (defocus, seeing) — the demo mode, and how the focus
  // meter is exercised without a telescope.
  async function simulate(body: {
    focus_offset_um?: number;
    seeing_arcsec?: number;
  }): Promise<void> {
    await apiPost("/api/device/live/simulate", body);
  }

  // --- mount ----------------------------------------------------------------------------------

  // slew sends the mount to J2000 coordinates. The engine refuses anything below its altitude floor
  // (pointing into the ground is how a tube meets a tripod leg), so a refusal here is a real answer,
  // not a glitch — it comes back as an error the caller shows.
  async function slew(
    raDeg: number,
    decDeg: number,
    force = false,
  ): Promise<void> {
    mount.value = await apiPost<DeviceMountState>("/api/device/mount/goto", {
      ra_deg: raDeg,
      dec_deg: decDeg,
      force,
    });
  }

  // stopMount is the STOP button: no arguments, no preconditions, works whenever the mount answers.
  async function stopMount(): Promise<void> {
    await apiPost("/api/device/mount/abort", {});
    await refreshMount();
  }

  async function setTracking(on: boolean, rate = "sidereal"): Promise<void> {
    mount.value = await apiPost<DeviceMountState>(
      "/api/device/mount/tracking",
      {
        on,
        rate,
      },
    );
  }

  // --- sequencer ------------------------------------------------------------------------------

  async function startSequence(body: {
    sequence: CaptureSequence;
    path: string;
    object?: string;
    panel?: string;
    focal_mm?: number;
    mosaic_plan_id?: number;
    tile_index?: number;
    dither_radius_px?: number;
    image_scale_arcsec_px?: number;
    ra_deg?: number;
    dec_deg?: number;
    measure_tracking?: boolean;
  }): Promise<void> {
    const res = await apiPost<{ progress: CaptureProgress }>(
      "/api/capture/start",
      { ...body, ...observerSite() },
    );
    progress.value = res.progress;
    watchProgress();
  }

  // The site the run is shot from, stamped on the session so its conditions (weather, Moon, sky
  // brightness) stay attributable to a place. The browser is the authority here: the engine's
  // configured location is right at home and wrong on every trip to a dark sky, and the sky store
  // already holds the location the user picked (and looked at the forecast for). Omitted entirely
  // when nothing has been picked, so the engine falls back to its own config.
  function observerSite(): { lat_deg?: number; lon_deg?: number } {
    const p = useSkyStore().params;
    if (typeof p.lat !== "number" || typeof p.lon !== "number") return {};
    return { lat_deg: p.lat, lon_deg: p.lon };
  }

  async function pause(): Promise<void> {
    progress.value = (
      await apiPost<{ progress: CaptureProgress }>("/api/capture/pause", {})
    ).progress;
  }
  async function resume(): Promise<void> {
    progress.value = (
      await apiPost<{ progress: CaptureProgress }>("/api/capture/resume", {})
    ).progress;
  }
  async function abort(): Promise<void> {
    progress.value = (
      await apiPost<{ progress: CaptureProgress }>("/api/capture/abort", {})
    ).progress;
  }

  async function refreshProgress(): Promise<void> {
    progress.value = (
      await apiGet<{ progress: CaptureProgress }>("/api/capture/status")
    ).progress;
  }

  // watchProgress streams the sequencer's state. It is reconnect-safe: the engine sends a snapshot
  // first, so re-opening the page mid-session shows the truth immediately.
  function watchProgress() {
    stopWatching();
    progressSource = new EventSource("/api/capture/events");
    progressSource.onmessage = (ev) => {
      try {
        progress.value = JSON.parse(ev.data) as CaptureProgress;
      } catch {
        // ignore a malformed frame
      }
    };
  }

  function stopWatching() {
    progressSource?.close();
    progressSource = null;
    window.clearInterval(deviceTimer);
    deviceTimer = 0;
  }

  // watchDevices polls the connected devices so their READ-ONLY values stay live.
  //
  // Sensor temperature and cooler power are only meaningful as a trend: you turn the cooler on and
  // watch the temperature fall. They were read once at connect and never again, so the cooling panel
  // showed a frozen number and 0 % power indefinitely — which reads as "the cooler control does not
  // work" even though the writes were landing correctly.
  //
  // Three seconds is plenty: a TEC pulls a degree over tens of seconds, and polling faster would put
  // avoidable USB traffic between the engine and a camera that is also streaming frames.
  const DEVICE_POLL_MS = 3000;
  let deviceTimer = 0;

  function watchDevices() {
    window.clearInterval(deviceTimer);
    deviceTimer = window.setInterval(() => {
      // Only the camera: the wheel and mount push their own state on the actions that change them,
      // and re-reading a moving wheel every few seconds buys nothing.
      if (!camera.value?.connected) return;
      void refreshCamera().catch(() => {
        // A transient read failure must not tear down the page; the next tick retries.
      });
    }, DEVICE_POLL_MS);
  }

  // watchMount subscribes to the mount's own event stream.
  //
  // Until now the mount's state was refreshed ONLY as a side effect of pressing a button, so
  // `slewing` never went back to false on its own and a link that died at two in the morning still
  // looked healthy until somebody clicked something. The stream also carries the serial link's
  // health, which is the whole point of watching it overnight.
  let mountSource: EventSource | null = null;

  function watchMount(): void {
    if (mountSource) return;
    mountSource = new EventSource("/api/device/mount/events");
    mountSource.onmessage = (e) => {
      try {
        mount.value = JSON.parse(e.data) as DeviceMountState;
      } catch {
        // A malformed frame is not worth tearing the page down for; the next one is a second away.
      }
    };
    mountSource.onerror = () => {
      // EventSource reconnects on its own. Nothing is surfaced here because the device server being
      // briefly unreachable is not the same as the MOUNT being unreachable, and conflating them
      // would put a scary message on the screen for a hot reload.
    };
  }

  function unwatchMount(): void {
    mountSource?.close();
    mountSource = null;
  }

  async function diagnoseMount(): Promise<MountDiagnosis> {
    return apiGet<MountDiagnosis>("/api/device/diagnose?probe=1");
  }

  async function setMountSite(
    latDeg: number,
    lonDeg: number,
  ): Promise<{ lat_deg: number; lon_deg: number }> {
    const res = await apiPost<{ site: { lat_deg: number; lon_deg: number } }>(
      "/api/device/mount/site",
      { lat_deg: latDeg, lon_deg: lonDeg },
    );
    return res.site;
  }

  async function setMountClock(zone?: string): Promise<{ utc: string }> {
    const res = await apiPost<{ clock: { utc: string } }>(
      "/api/device/mount/clock",
      {
        zone: zone || Intl.DateTimeFormat().resolvedOptions().timeZone,
      },
    );
    return res.clock;
  }

  // What is stored in the mount right now. Read on demand, never polled: it is ninety round trips on
  // a 9600-baud link, and the mount has other things to do with them.
  async function auditMount(): Promise<{
    connected: boolean;
    audit?: MountAudit;
  }> {
    return apiGet<{ connected: boolean; audit?: MountAudit }>(
      "/api/device/mount/audit",
    );
  }

  // Put back what this app can have written. A dry run unless apply is set — the server defaults the
  // same way, and both are deliberate on a call that changes hardware state outliving the session.
  async function resetMount(
    body: Record<string, unknown>,
  ): Promise<MountRestoreResult> {
    return apiPost<MountRestoreResult>("/api/device/mount/reset", body);
  }

  async function loadSessions(): Promise<void> {
    sessions.value = (
      await apiGet<{ sessions: CaptureSessionRow[] }>("/api/capture/sessions")
    ).sessions;
  }

  async function loadSequences(): Promise<void> {
    sequences.value = (
      await apiGet<{
        sequences: { id: number; name: string; payload: CaptureSequence }[];
      }>("/api/capture/sequences")
    ).sequences;
  }

  async function saveSequence(
    name: string,
    sequence: CaptureSequence,
  ): Promise<void> {
    await apiPost("/api/capture/sequences", { name, sequence });
    await loadSequences();
  }

  return {
    deviceStatus,
    deviceList,
    camera,
    wheel,
    mount,
    error,
    connected,
    liveRunning,
    liveImage,
    liveStats,
    liveExposureEnds,
    liveExposureUs,
    liveError,
    progress,
    running,
    sessions,
    sequences,
    refreshDevices,
    refreshDeviceList,
    refreshCamera,
    refreshWheel,
    refreshMount,
    connectCamera,
    connectWheel,
    connectMount,
    disconnect,
    setControl,
    controlValue,
    setFilter,
    startLive,
    stopLive,
    fetchCrop,
    simulate,
    slew,
    stopMount,
    setTracking,
    startSequence,
    pause,
    resume,
    abort,
    refreshProgress,
    watchProgress,
    stopWatching,
    watchDevices,
    watchMount,
    unwatchMount,
    diagnoseMount,
    setMountSite,
    setMountClock,
    auditMount,
    resetMount,
    loadSessions,
    loadSequences,
    saveSequence,
  };
});
