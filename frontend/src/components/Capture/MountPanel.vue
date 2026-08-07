<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, input } from "@/constants/styles";
import { apiGet, apiPost } from "@/services/api";
import { useCaptureStore } from "@/stores/capture";
import { useSkyStore } from "@/stores/sky";
import type { MountDiagnosis } from "@/types";
import { decToDMS, raToHMS } from "@/utils/sexagesimal";

// Manual mount control: pick the port, nudge it around, turn tracking on and off, stop it.
//
// Everything here can physically move a telescope, so the panel is blunt about state: it names the
// port it is talking to, shows whether the mount considers itself aligned (an unaligned GoTo is
// refused outright), and keeps STOP visible and enabled at all times.
const { t } = useI18n();
const store = useCaptureStore();
const sky = useSkyStore();

interface PortInfo {
  path: string;
  label: string;
  likely: boolean;
}

const ports = ref<PortInfo[]>([]);
const selectedPort = ref("");
const rate = ref(5);
const error = ref("");
const diagnosis = ref<MountDiagnosis | null>(null);
const diagnosing = ref(false);
const siteLat = ref(sky.params.lat ?? 0);
const siteLon = ref(sky.params.lon ?? 0);
const zone = ref(Intl.DateTimeFormat().resolvedOptions().timeZone);
const siteResult = ref("");

const mount = computed(() => store.mount?.mount ?? null);
const connected = computed(() => !!store.connected.mount);
const link = computed(() => store.mount?.link ?? null);
// Connected is not the same as connected to a telescope. The devices panel can leave the simulator
// attached, and the simulator implements neither site nor clock — so those buttons answer "not
// supported by this device" while a perfectly good hand controller sits on the USB bus. Keep the
// port picker reachable in that state; hiding it made disconnecting the only way back to hardware.
const simulated = computed(
  () => connected.value && mount.value?.driver === "sim",
);

onMounted(() => {
  document.addEventListener("visibilitychange", onVisibilityChange);
  void loadPorts();
  // The stream carries position, slewing AND the serial link's health. Without it the panel only
  // ever updated when somebody pressed a button, which is no use at all overnight.
  store.watchMount();
});
onBeforeUnmount(() => {
  document.removeEventListener("visibilitychange", onVisibilityChange);
  releaseAll();
  stopRenewal();
  store.unwatchMount();
});

async function loadPorts() {
  try {
    const res = await apiGet<{ ports: PortInfo[] }>("/api/device/ports");
    ports.value = res.ports ?? [];
    selectedPort.value =
      ports.value.find((p) => p.likely)?.path ?? ports.value[0]?.path ?? "";
  } catch {
    ports.value = [];
  }
}

async function connectReal() {
  error.value = "";
  try {
    await store.connectMount("nexstar", selectedPort.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

// The hand controller's USB socket is a Prolific bridge and macOS ships no driver for it, so "no
// port" can mean a missing system extension, a discontinued chip, an unpowered mount or a
// charge-only cable. Only the USB bus can tell those apart, which is what this asks the server.
async function diagnose() {
  diagnosing.value = true;
  error.value = "";
  try {
    diagnosis.value = await store.diagnoseMount();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    diagnosing.value = false;
  }
}

async function sendSite() {
  siteResult.value = "";
  error.value = "";
  try {
    const got = await store.setMountSite(siteLat.value, siteLon.value);
    siteResult.value = t("capture.mount.mountSite", {
      lat: got.lat_deg.toFixed(4),
      lon: got.lon_deg.toFixed(4),
    });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

async function sendClock() {
  siteResult.value = "";
  error.value = "";
  try {
    const got = await store.setMountClock(zone.value);
    siteResult.value = t("capture.mount.mountClock", {
      utc: new Date(got.utc).toISOString().slice(0, 19).replace("T", " "),
    });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

// The jog deadman, from this side.
//
// A pointerup that never arrives — a closed tab, a slept laptop, a dropped network — used to leave
// the mount slewing until it hit something. The server now stops an axis that is not renewed within
// hold_ms, so holding a button means saying so repeatedly rather than once.
const HOLD_MS = 4000;
const RENEW_MS = 1500;
let renewTimer = 0;

// A mount has one motor per axis. That is the whole rule: both can run at once — which is how the
// hand controller slews diagonally — but neither can run two ways. So what is held is tracked PER
// AXIS rather than as a single direction, and north+west drives a diagonal while north+south is a
// contradiction that resolves to whichever arrow was pressed later.
type Axis = "ra" | "dec";

const AXIS_OF: Record<string, Axis> = {
  north: "dec",
  south: "dec",
  east: "ra",
  west: "ra",
};

const held = reactive<Record<Axis, string>>({ ra: "", dec: "" });

const heldDirections = computed(() =>
  [held.dec, held.ra].filter((d): d is string => !!d),
);

function isHeld(direction: string) {
  return held[AXIS_OF[direction]] === direction;
}

// One timer renews every axis still held.
//
// The server arms its deadman PER AXIS, so an axis nobody renews stops on its own while the other
// keeps running. Renewing only the most recent direction — which is what a single held-direction
// variable can do — would therefore drop the first axis four seconds into a diagonal, and it would
// look like the mount had decided to stop half the movement by itself.
function startRenewal() {
  if (renewTimer) return;
  renewTimer = window.setInterval(() => {
    for (const direction of heldDirections.value) {
      void apiPost("/api/device/mount/jog", {
        direction,
        rate: rate.value,
        hold_ms: HOLD_MS,
      }).catch(() => {
        // The deadman is the safety net; a missed renewal simply stops the axis.
      });
    }
  }, RENEW_MS);
}

function stopRenewal() {
  window.clearInterval(renewTimer);
  renewTimer = 0;
}

async function jog(direction: string) {
  error.value = "";
  const axis = AXIS_OF[direction];
  if (!axis) return;
  // One motor cannot run both ways, so a reversal stops the axis before restarting it. The other
  // axis is left alone.
  if (held[axis] && held[axis] !== direction) await stopAxis(held[axis]);
  try {
    await apiPost("/api/device/mount/jog", {
      direction,
      rate: rate.value,
      hold_ms: HOLD_MS,
    });
    held[axis] = direction;
    startRenewal();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

// stopAxis halts one motor and leaves the other running — releasing one arrow of a diagonal should
// straighten the movement, not end it. A rate-0 frame stops the MOTOR the direction belongs to
// whichever way that direction pointed, so the direction only has to name the right axis.
async function stopAxis(direction: string) {
  const axis = AXIS_OF[direction];
  if (!axis) return;
  held[axis] = "";
  if (!heldDirections.value.length) stopRenewal();
  try {
    await apiPost("/api/device/mount/jog", { direction, rate: 0 });
  } catch {
    // the STOP button below is the real safety net
  }
}

async function stopJog() {
  stopRenewal();
  held.ra = "";
  held.dec = "";
  try {
    // North and east only, and that is not an oversight: a rate-0 frame stops that MOTOR whatever
    // direction byte it carries, and north/east cover both motors. Adding south and west would send
    // two redundant frames down a 9600-baud link for no gain.
    for (const direction of ["north", "east"]) {
      await apiPost("/api/device/mount/jog", { direction, rate: 0 });
    }
  } catch {
    // the STOP button below is the real safety net
  }
}

// Keyboard jogging.
//
// The arrows map to the pad's own layout: up/down are the declination motor, left/right the
// right-ascension one (left is East, matching the "← E" button). Holding a key behaves exactly like
// holding a button, renewal and all, because both go through jog()/stopAxis().
//
// Two arrows on different axes are held together deliberately: the hand controller slews diagonally
// and this pad now does too. Two arrows on the SAME axis still cannot both apply, and the later one
// wins rather than the earlier — pressing east while west is held reverses, which is what the key
// press plainly asks for.
const KEY_DIRECTION: Record<string, string> = {
  ArrowUp: "north",
  ArrowDown: "south",
  ArrowLeft: "east",
  ArrowRight: "west",
};

function onPadKeyDown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    // Escape is the keyboard's STOP, and it must work whatever else is going on.
    e.preventDefault();
    void stopJog();
    void store.stopMount();
    return;
  }
  const direction = KEY_DIRECTION[e.key];
  if (!direction) return;
  // Without this the arrows scroll the page under the panel while the mount moves.
  e.preventDefault();
  // Auto-repeat fires this many times a second while a key is held. The renewal interval already
  // keeps the axis alive, so repeats are ignored rather than turned into a flood of commands down a
  // 9600-baud link.
  if (e.repeat || isHeld(direction)) return;
  void jog(direction);
}

function onPadKeyUp(e: KeyboardEvent) {
  const direction = KEY_DIRECTION[e.key];
  if (!direction) return;
  e.preventDefault();
  // Only the released axis stops. Letting go of one arrow of a diagonal leaves the other running,
  // exactly as it does on the hand controller.
  if (!isHeld(direction)) return;
  void stopAxis(direction);
}

// releaseAll is the answer to every way a keyup can go missing — focus lost, tab switched, laptop
// slept. Everything stops, because what was held is no longer knowable. The server-side deadman is
// still the last line, but stopping here means the mount halts in milliseconds rather than in four
// seconds.
function releaseAll() {
  if (!heldDirections.value.length) return;
  void stopJog();
}

function onVisibilityChange() {
  if (document.hidden) releaseAll();
}

function ms(v: number | undefined): string {
  return v === undefined ? "?" : `${Math.round(v)}ms`;
}

function uptime(v: number | undefined): string {
  if (!v) return "—";
  const s = Math.floor(v / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60)}m`;
}
</script>

<template>
  <div class="space-y-2">
    <!-- Port + connection -->
    <div v-if="!connected || simulated" class="space-y-1">
      <p
        v-if="simulated"
        class="rounded-md border border-amber-400/50 bg-amber-50 p-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
      >
        {{ t("capture.mount.simulatedHint") }}
      </p>
      <label class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("capture.mount.port") }}
        <select v-model="selectedPort" :class="input" class="mt-0.5">
          <option v-for="p in ports" :key="p.path" :value="p.path">
            {{ p.label }}{{ p.likely ? " ★" : "" }}
          </option>
        </select>
      </label>
      <p v-if="!ports.length" class="text-[11px] text-slate-400">
        {{ t("capture.mount.noPorts") }}
      </p>
      <div class="flex gap-2">
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          :disabled="!selectedPort"
          @click="connectReal"
        >
          {{ t("capture.mount.connect") }}
        </button>
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="loadPorts"
        >
          {{ t("capture.mount.rescan") }}
        </button>
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          :disabled="diagnosing"
          @click="diagnose"
        >
          {{
            diagnosing
              ? t("capture.mount.diagnosing")
              : t("capture.mount.diagnose")
          }}
        </button>
      </div>

      <!-- The answer to "why is there no port": the chip on the USB bus, and what to do about it. -->
      <div
        v-if="diagnosis"
        class="rounded-md border border-slate-200 p-2 text-[11px] dark:border-slate-700"
      >
        <div class="font-mono text-slate-500 dark:text-slate-400">
          {{ diagnosis.verdict
          }}<template v-if="diagnosis.chip"> · {{ diagnosis.chip }}</template>
        </div>
        <p class="mt-1 text-slate-600 dark:text-slate-300">
          {{ diagnosis.detail }}
        </p>
      </div>
    </div>

    <!-- Live state -->
    <div v-else class="space-y-2">
      <div class="text-xs text-slate-500 dark:text-slate-400">
        <div class="font-mono">
          {{ raToHMS(mount?.ra_deg ?? 0) }} ·
          {{ decToDMS(mount?.dec_deg ?? 0) }}
        </div>
        <div class="flex flex-wrap gap-2">
          <span v-if="mount"
            >{{ Math.round(mount.alt_deg) }}° {{ t("capture.mount.up") }}</span
          >
          <span
            v-if="mount && !mount.aligned"
            class="text-amber-600 dark:text-amber-400"
            >{{ t("capture.mount.notAligned") }}</span
          >
          <span
            v-if="mount?.slewing"
            class="text-brand-600 dark:text-brand-300"
            >{{ t("capture.mount.slewing") }}</span
          >
          <span v-if="mount?.pier_side">{{
            t(`capture.mount.pier_${mount.pier_side}`)
          }}</span>
        </div>
      </div>

      <!-- Jog pad. The wrapper is focusable so the arrow keys can drive the same commands as the
           buttons; blurring it stops whatever is moving. -->
      <div
        tabindex="0"
        role="group"
        :aria-label="t('capture.mount.padLabel')"
        class="inline-block rounded-md outline-none ring-brand-500 focus:ring-2"
        @keydown="onPadKeyDown"
        @keyup="onPadKeyUp"
        @blur="releaseAll"
      >
        <div class="grid w-32 grid-cols-3 gap-1">
          <span />
          <button
            :class="[btnGhost, isHeld('north') ? 'ring-2 ring-brand-500' : '']"
            class="!px-1 !py-1 text-xs"
            @pointerdown="jog('north')"
            @pointerup="stopAxis('north')"
            @pointercancel="stopAxis('north')"
            @pointerleave="stopAxis('north')"
          >
            ↑ N
          </button>
          <span />
          <button
            :class="[btnGhost, isHeld('east') ? 'ring-2 ring-brand-500' : '']"
            class="!px-1 !py-1 text-xs"
            @pointerdown="jog('east')"
            @pointerup="stopAxis('east')"
            @pointercancel="stopAxis('east')"
            @pointerleave="stopAxis('east')"
          >
            ← E
          </button>
          <button
            class="rounded-md border border-danger-500 px-1 py-1 text-[11px] font-semibold text-danger-500 hover:bg-danger-500/10"
            @click="store.stopMount()"
          >
            {{ t("capture.mount.stop") }}
          </button>
          <button
            :class="[btnGhost, isHeld('west') ? 'ring-2 ring-brand-500' : '']"
            class="!px-1 !py-1 text-xs"
            @pointerdown="jog('west')"
            @pointerup="stopAxis('west')"
            @pointercancel="stopAxis('west')"
            @pointerleave="stopAxis('west')"
          >
            W →
          </button>
          <span />
          <button
            :class="[btnGhost, isHeld('south') ? 'ring-2 ring-brand-500' : '']"
            class="!px-1 !py-1 text-xs"
            @pointerdown="jog('south')"
            @pointerup="stopAxis('south')"
            @pointercancel="stopAxis('south')"
            @pointerleave="stopAxis('south')"
          >
            ↓ S
          </button>
          <span />
        </div>
        <p class="mt-1 text-[11px] text-slate-400">
          {{ t("capture.mount.padHint") }}
        </p>
      </div>

      <div class="flex flex-wrap items-center gap-2 text-xs">
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          {{ t("capture.mount.rate") }}
          <input
            v-model.number="rate"
            type="range"
            min="1"
            max="9"
            class="w-20 accent-brand-600"
          />
          <span class="w-3 font-mono">{{ rate }}</span>
        </label>
        <button
          :class="btnGhost"
          class="!px-2 !py-1"
          @click="store.setTracking(!mount?.tracking)"
        >
          {{
            mount?.tracking
              ? t("capture.mount.trackingOff")
              : t("capture.mount.trackingOn")
          }}
        </button>
      </div>

      <p v-if="mount?.model" class="text-[11px] text-slate-400">
        {{ mount.model
        }}<template v-if="mount.firmware"> · {{ mount.firmware }}</template>
      </p>

      <!-- Link health. Absent for the simulator, which has no serial link — inventing latency there
           would make the panel lie in exactly the case where you are checking sim versus hardware. -->
      <div
        v-if="link"
        class="rounded-md border border-slate-200 p-2 text-[11px] dark:border-slate-700"
      >
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-medium text-slate-600 dark:text-slate-300">
            {{ t("capture.mount.linkTitle") }}
          </span>
          <span
            v-if="link.reconnecting"
            class="rounded bg-amber-500/15 px-1.5 py-0.5 text-amber-600 dark:text-amber-400"
          >
            {{ t("capture.mount.reconnecting") }}
          </span>
          <span class="font-mono text-slate-400">{{ link.path }}</span>
        </div>
        <div
          class="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-slate-500 dark:text-slate-400"
        >
          <span
            >{{ t("capture.mount.uptime") }} {{ uptime(link.uptime_ms) }}</span
          >
          <span>
            {{ t("capture.mount.latency") }} {{ ms(link.latency_p50_ms) }} /
            {{ ms(link.latency_p99_ms) }}
          </span>
          <span>{{ t("capture.mount.commands") }} {{ link.commands }}</span>
          <span
            :class="link.errors ? 'text-amber-600 dark:text-amber-400' : ''"
          >
            {{ t("capture.mount.errors") }} {{ link.errors }}
          </span>
          <span
            :class="link.resyncs ? 'text-amber-600 dark:text-amber-400' : ''"
          >
            {{ t("capture.mount.resyncs") }} {{ link.resyncs }}
          </span>
          <span
            :class="link.reconnects ? 'text-amber-600 dark:text-amber-400' : ''"
          >
            {{ t("capture.mount.reconnects") }} {{ link.reconnects }}
          </span>
        </div>
        <p
          v-if="link.last_error"
          class="mt-1 truncate text-slate-400"
          :title="link.last_error"
        >
          {{ link.last_error }}
        </p>
      </div>

      <!-- Site and clock. The hand controller computes every alt-azimuth from these, so a clock an
           hour out points the telescope fifteen degrees away and nothing in the image says why. -->
      <details
        class="rounded-md border border-slate-200 p-2 dark:border-slate-700"
      >
        <summary
          class="cursor-pointer text-xs text-slate-600 dark:text-slate-300"
        >
          {{ t("capture.mount.siteTitle") }}
        </summary>
        <p class="mt-1 text-[11px] text-slate-400">
          {{ t("capture.mount.siteHint") }}
        </p>
        <div class="mt-2 grid grid-cols-2 gap-2 text-[11px]">
          <label class="text-slate-500 dark:text-slate-400">
            {{ t("capture.mount.lat") }}
            <input
              v-model.number="siteLat"
              type="number"
              step="0.0001"
              :class="input"
            />
          </label>
          <label class="text-slate-500 dark:text-slate-400">
            {{ t("capture.mount.lon") }}
            <input
              v-model.number="siteLon"
              type="number"
              step="0.0001"
              :class="input"
            />
          </label>
          <label class="col-span-2 text-slate-500 dark:text-slate-400">
            {{ t("capture.mount.timezone") }}
            <input v-model="zone" type="text" :class="input" />
          </label>
        </div>
        <div class="mt-2 flex gap-2">
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            @click="sendSite"
          >
            {{ t("capture.mount.applySite") }}
          </button>
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            @click="sendClock"
          >
            {{ t("capture.mount.applyClock") }}
          </button>
        </div>
        <p
          v-if="siteResult"
          class="mt-1 text-[11px] text-emerald-600 dark:text-emerald-400"
        >
          {{ siteResult }}
        </p>
      </details>
    </div>

    <p v-if="error" class="text-xs text-danger-500">{{ error }}</p>
  </div>
</template>
