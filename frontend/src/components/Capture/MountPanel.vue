<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, input } from "@/constants/styles";
import { apiGet, apiPost } from "@/services/api";
import { useCaptureStore } from "@/stores/capture";
import { decToDMS, raToHMS } from "@/utils/sexagesimal";

// Manual mount control: pick the port, nudge it around, turn tracking on and off, stop it.
//
// Everything here can physically move a telescope, so the panel is blunt about state: it names the
// port it is talking to, shows whether the mount considers itself aligned (an unaligned GoTo is
// refused outright), and keeps STOP visible and enabled at all times.
const { t } = useI18n();
const store = useCaptureStore();

interface PortInfo {
  path: string;
  label: string;
  likely: boolean;
}

const ports = ref<PortInfo[]>([]);
const selectedPort = ref("");
const rate = ref(5);
const error = ref("");

const mount = computed(() => store.mount?.mount ?? null);
const connected = computed(() => !!store.connected.mount);

onMounted(loadPorts);

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

async function jog(direction: string) {
  error.value = "";
  try {
    await apiPost("/api/device/mount/jog", { direction, rate: rate.value });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

async function stopJog() {
  try {
    for (const direction of ["north", "east"]) {
      await apiPost("/api/device/mount/jog", { direction, rate: 0 });
    }
  } catch {
    // the STOP button below is the real safety net
  }
}
</script>

<template>
  <div class="space-y-2">
    <!-- Port + connection -->
    <div v-if="!connected" class="space-y-1">
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

      <!-- Jog pad -->
      <div class="grid w-32 grid-cols-3 gap-1">
        <span />
        <button
          :class="btnGhost"
          class="!px-1 !py-1 text-xs"
          @pointerdown="jog('north')"
          @pointerup="stopJog"
        >
          ↑ N
        </button>
        <span />
        <button
          :class="btnGhost"
          class="!px-1 !py-1 text-xs"
          @pointerdown="jog('east')"
          @pointerup="stopJog"
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
          :class="btnGhost"
          class="!px-1 !py-1 text-xs"
          @pointerdown="jog('west')"
          @pointerup="stopJog"
        >
          W →
        </button>
        <span />
        <button
          :class="btnGhost"
          class="!px-1 !py-1 text-xs"
          @pointerdown="jog('south')"
          @pointerup="stopJog"
        >
          ↓ S
        </button>
        <span />
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
    </div>

    <p v-if="error" class="text-xs text-danger-500">{{ error }}</p>
  </div>
</template>
