<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from "vue";
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
  const driver = driversFor(kind)[0] ?? "sim";
  try {
    if (kind === "camera") await store.connectCamera(driver);
    if (kind === "wheel") await store.connectWheel(driver);
    if (kind === "mount") await store.connectMount(driver);
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
