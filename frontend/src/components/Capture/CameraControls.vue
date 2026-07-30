<script setup lang="ts">
import AdvancedControls from "@/components/Capture/AdvancedControls.vue";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, input } from "@/constants/styles";
import { useCaptureStore } from "@/stores/capture";

// Exposure, gain, offset, cooling and the filter wheel.
//
// Every range here comes from the DRIVER (min/max/default as the camera reports them), never from a
// constant in this file: a different camera has different limits, and firmware changes them. The
// exposure control in particular spans nine orders of magnitude — 32 µs for a planetary frame to
// 2000 s for narrowband — so it is entered with a unit rather than as a raw microsecond count.
const { t } = useI18n();
const store = useCaptureStore();

const units = [
  { key: "us", factor: 1, label: "µs" },
  { key: "ms", factor: 1000, label: "ms" },
  { key: "s", factor: 1_000_000, label: "s" },
];
const unit = ref(units[2]);
const exposureValue = ref(1);
const gain = ref(0);
const offset = ref(0);
const targetTemp = ref(-15);

const caps = computed(() => store.camera?.caps);
const controls = computed(() => store.camera?.controls ?? []);

function control(name: string) {
  return controls.value.find((c) => c.name === name);
}

// Seed the inputs from the camera whenever it (re)connects, so the form always opens showing what
// the hardware is actually set to.
watch(
  () => store.camera?.connected,
  (connected) => {
    if (!connected) return;
    const exp = control("exposure")?.value ?? 1_000_000;
    unit.value =
      exp >= 1_000_000 ? units[2] : exp >= 1000 ? units[1] : units[0];
    exposureValue.value = exp / unit.value.factor;
    gain.value = control("gain")?.value ?? 0;
    offset.value = control("offset")?.value ?? 0;
    targetTemp.value = control("target_temp")?.value ?? -15;
  },
  { immediate: true },
);

const exposureUs = computed(() =>
  Math.round(exposureValue.value * unit.value.factor),
);

const exposureRange = computed(() => {
  const c = control("exposure");
  return {
    min: c?.min ?? 32,
    max: c?.max ?? 2_000_000_000,
  };
});

const exposureError = computed(() => {
  const { min, max } = exposureRange.value;
  if (exposureUs.value < min) return t("capture.controls.expTooShort", { min });
  if (exposureUs.value > max) return t("capture.controls.expTooLong");
  return "";
});

const presets = [
  { label: "32 µs", us: 32 },
  { label: "1 ms", us: 1000 },
  { label: "100 ms", us: 100_000 },
  { label: "1 s", us: 1_000_000 },
  { label: "30 s", us: 30_000_000 },
  { label: "120 s", us: 120_000_000 },
];

function applyPreset(us: number) {
  unit.value = us >= 1_000_000 ? units[2] : us >= 1000 ? units[1] : units[0];
  exposureValue.value = us / unit.value.factor;
  void apply("exposure", us);
}

async function apply(name: string, value: number) {
  try {
    await store.setControl(name, value);
  } catch (e) {
    store.error = e instanceof Error ? e.message : String(e);
  }
}

const cooler = computed(() => control("cooler_on"));
const coolerOn = computed(() => (cooler.value?.value ?? 0) !== 0);
const sensorTemp = computed(() => {
  const c = control("temperature");
  if (!c) return null;
  return c.value / (c.scale_divisor || 1);
});
const coolerPower = computed(() => control("cooler_power")?.value ?? 0);

// One entry per slot the wheel actually has, whatever the names list happens to contain: an unnamed
// slot is still a slot you can move to, and a name beyond the last slot is not.
const wheelNames = computed(() => {
  const w = store.wheel?.wheel;
  const n = w?.slots ?? 0;
  const names = w?.names ?? [];
  return Array.from({ length: n }, (_, i) => names[i] || `${i + 1}`);
});
</script>

<template>
  <div class="space-y-4">
    <p v-if="!store.camera?.connected" class="text-sm text-slate-400">
      {{ t("capture.controls.noCamera") }}
    </p>

    <template v-else>
      <!-- Exposure -->
      <div>
        <span
          class="text-sm font-semibold text-slate-700 dark:text-slate-200"
          >{{ t("capture.controls.exposure") }}</span
        >
        <div class="mt-1 flex items-center gap-2">
          <input
            v-model.number="exposureValue"
            type="number"
            min="0"
            step="any"
            :class="input"
            class="w-28"
            @change="apply('exposure', exposureUs)"
          />
          <select v-model="unit" :class="input" class="w-20">
            <option v-for="u in units" :key="u.key" :value="u">
              {{ u.label }}
            </option>
          </select>
        </div>
        <div class="mt-1 flex flex-wrap gap-1">
          <button
            v-for="p in presets"
            :key="p.us"
            :class="btnGhost"
            class="!px-1.5 !py-0.5 text-[11px]"
            @click="applyPreset(p.us)"
          >
            {{ p.label }}
          </button>
        </div>
        <p v-if="exposureError" class="mt-1 text-xs text-danger-500">
          {{ exposureError }}
        </p>
        <p v-else class="mt-1 text-[11px] text-slate-400">
          {{
            t("capture.controls.expRange", {
              min: exposureRange.min,
              max: Math.round(exposureRange.max / 1_000_000),
            })
          }}
        </p>
      </div>

      <!-- Gain / offset, ranges straight from the driver -->
      <div class="grid grid-cols-2 gap-3">
        <label class="text-xs text-slate-500 dark:text-slate-400">
          {{ t("capture.controls.gain") }}
          <input
            v-model.number="gain"
            type="number"
            :min="control('gain')?.min ?? 0"
            :max="control('gain')?.max ?? 600"
            :class="input"
            @change="apply('gain', gain)"
          />
          <span class="text-[10px]"
            >{{ control("gain")?.min }}–{{ control("gain")?.max }}</span
          >
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400">
          {{ t("capture.controls.offset") }}
          <input
            v-model.number="offset"
            type="number"
            :min="control('offset')?.min ?? 0"
            :max="control('offset')?.max ?? 255"
            :class="input"
            @change="apply('offset', offset)"
          />
          <span class="text-[10px]"
            >{{ control("offset")?.min }}–{{ control("offset")?.max }}</span
          >
        </label>
      </div>

      <!-- Cooling -->
      <div v-if="caps?.has_cooler">
        <span
          class="text-sm font-semibold text-slate-700 dark:text-slate-200"
          >{{ t("capture.controls.cooling") }}</span
        >
        <div class="mt-1 flex flex-wrap items-center gap-2 text-xs">
          <label
            class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
          >
            {{ t("capture.controls.target") }}
            <input
              v-model.number="targetTemp"
              type="number"
              :min="control('target_temp')?.min ?? -40"
              :max="control('target_temp')?.max ?? 30"
              :class="input"
              class="w-20"
              @change="apply('target_temp', targetTemp)"
            />
          </label>
          <button
            :class="btnGhost"
            class="!px-2 !py-1"
            @click="apply('cooler_on', coolerOn ? 0 : 1)"
          >
            {{
              coolerOn
                ? t("capture.controls.coolerOff")
                : t("capture.controls.coolerOn")
            }}
          </button>
          <span
            v-if="sensorTemp !== null"
            class="font-mono text-slate-500 dark:text-slate-400"
          >
            {{ sensorTemp.toFixed(1) }} °C · {{ coolerPower }}%
          </span>
        </div>
      </div>

      <!-- Filter wheel -->
      <div v-if="store.wheel?.connected">
        <span
          class="text-sm font-semibold text-slate-700 dark:text-slate-200"
          >{{ t("capture.controls.filter") }}</span
        >
        <div class="mt-1 flex flex-wrap gap-1">
          <button
            v-for="(name, i) in wheelNames"
            :key="name"
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            :disabled="store.wheel?.wheel?.moving"
            :aria-pressed="store.wheel?.wheel?.position === i + 1"
            @click="store.setFilter(i + 1)"
          >
            <span
              :class="
                store.wheel?.wheel?.position === i + 1
                  ? 'font-semibold text-brand-600 dark:text-brand-300'
                  : ''
              "
              >{{ name }}</span
            >
          </button>
          <span
            v-if="store.wheel?.wheel?.moving"
            class="self-center text-xs text-slate-400"
            >{{ t("capture.controls.wheelMoving") }}</span
          >
        </div>
      </div>
    </template>
  </div>

  <!-- Everything else the camera reports: USB bandwidth, fan, anti-dew, binning modes… -->
  <AdvancedControls />
</template>
