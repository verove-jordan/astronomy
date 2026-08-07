<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost } from "@/constants/styles";
import { useCaptureStore } from "@/stores/capture";

// Which devices are attached, and the connect/disconnect controls.
//
// The device server is a separate process, so "not running" is a normal state with an actionable
// answer rather than an error — the panel says exactly what to start.
const { t } = useI18n();
const store = useCaptureStore();

const REFRESH_MS = 3000;
let timer: number | undefined;

onMounted(() => {
  void store.refreshDevices();
  timer = window.setInterval(() => void store.refreshDevices(), REFRESH_MS);
});
onBeforeUnmount(() => window.clearInterval(timer));

const drivers = computed(() => store.deviceStatus?.health?.drivers ?? []);
const available = computed(() => drivers.value.filter((d) => d.available));

// Only real drivers are offered per kind; "sim" always is, which is what makes the whole page
// usable with nothing plugged in.
function driversFor(kind: string) {
  return available.value
    .filter((d) => d.kind === "all" || d.kind === kind)
    .map((d) => d.name);
}

// The devices a driver has actually FOUND, which is a different question from which drivers exist.
// The driver report always lists the simulator first, so picking driversFor(kind)[0] made this panel
// structurally incapable of reaching hardware: connecting the mount opened a simulated AVX even with
// a hand controller on the USB bus, and the simulator implements neither site nor clock — which is
// how "not supported by this device" ended up being the answer to a perfectly good serial link.
function discoveredFor(kind: string) {
  return store.deviceList.filter((d) => d.kind === kind && d.driver !== "sim");
}

// An explicit choice always wins; otherwise prefer real hardware and fall back to the simulator.
const chosen = reactive<Record<string, string>>({});

function driverFor(kind: string): string {
  return (
    chosen[kind] ??
    discoveredFor(kind)[0]?.driver ??
    driversFor(kind)[0] ??
    "sim"
  );
}

const rows = computed(() => [
  {
    kind: "camera" as const,
    label: t("capture.device.camera"),
    connected: store.connected.camera,
    detail: store.camera?.caps?.name ?? "",
  },
  {
    kind: "wheel" as const,
    label: t("capture.device.wheel"),
    connected: store.connected.wheel,
    detail: store.wheel?.wheel
      ? t("capture.device.wheelDetail", {
          slots: store.wheel.wheel.slots,
          position: store.wheel.wheel.position,
        })
      : "",
  },
  {
    kind: "mount" as const,
    label: t("capture.device.mount"),
    connected: store.connected.mount,
    detail: store.mount?.mount?.model ?? "",
  },
]);

async function connect(kind: "camera" | "wheel" | "mount") {
  const driver = driverFor(kind);
  try {
    if (kind === "camera") await store.connectCamera(driver);
    if (kind === "wheel") await store.connectWheel(driver);
    if (kind === "mount") {
      // A discovered mount is identified by its serial path; passing it means the driver opens the
      // adapter we listed rather than re-guessing which one is the telescope.
      const port = discoveredFor("mount").find((d) => d.driver === driver)?.id;
      await store.connectMount(driver, port);
    }
  } catch (e) {
    store.error = e instanceof Error ? e.message : String(e);
  }
}
</script>

<template>
  <div class="space-y-2">
    <div
      v-if="store.deviceStatus && !store.deviceStatus.running"
      class="rounded-md border border-amber-400/50 bg-amber-50 p-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
    >
      {{ t("capture.device.notRunning") }}
      <code class="rounded bg-black/10 px-1 font-mono dark:bg-white/10"
        >just device</code
      >
    </div>

    <div
      v-for="row in rows"
      :key="row.kind"
      class="flex flex-wrap items-center gap-2 text-sm"
    >
      <span
        class="h-2 w-2 shrink-0 rounded-full"
        :class="row.connected ? 'bg-success' : 'bg-slate-400'"
      />
      <span class="w-20 shrink-0 text-slate-600 dark:text-slate-300">{{
        row.label
      }}</span>
      <span class="min-w-0 flex-1 truncate text-xs text-slate-400">{{
        row.detail
      }}</span>
      <select
        v-if="!row.connected && driversFor(row.kind).length > 1"
        class="shrink-0 rounded-md border border-slate-300 bg-white px-1.5 py-1 text-xs text-slate-900 focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500 dark:border-brand-800/60 dark:bg-brand-900/20 dark:text-slate-100"
        :value="driverFor(row.kind)"
        :disabled="!store.deviceStatus?.running"
        :aria-label="t('capture.device.driverFor', { device: row.label })"
        @change="chosen[row.kind] = ($event.target as HTMLSelectElement).value"
      >
        <option v-for="d in driversFor(row.kind)" :key="d" :value="d">
          {{ d }}
        </option>
      </select>
      <button
        :class="btnGhost"
        class="!px-2 !py-1 text-xs"
        :disabled="!store.deviceStatus?.running"
        @click="row.connected ? store.disconnect(row.kind) : connect(row.kind)"
      >
        {{
          row.connected
            ? t("capture.device.disconnect")
            : t("capture.device.connect")
        }}
      </button>
    </div>

    <p v-if="available.length" class="text-[11px] text-slate-400">
      {{
        t("capture.device.drivers", {
          list: available.map((d) => d.name).join(", "),
        })
      }}
    </p>
    <p v-if="store.error" class="text-xs text-danger-500">{{ store.error }}</p>
  </div>
</template>
