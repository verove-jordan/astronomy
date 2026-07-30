<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { input } from "@/constants/styles";
import { useCaptureStore } from "@/stores/capture";
import type { DeviceControl } from "@/types";

// Everything else the camera can do.
//
// ASICAP exposes a long tail of controls beyond exposure/gain/offset/cooling, and a couple of them
// matter more than their obscurity suggests — USB bandwidth above all, because it is the knob you
// lower when a planetary run starts dropping frames. So this renders whatever the camera REPORTS
// rather than a hardcoded list: a control we have never seen still appears, with the range the
// driver read off the device.
const { t } = useI18n();
const store = useCaptureStore();

const open = ref(false);
const busy = ref("");

const all = computed<DeviceControl[]>(() => store.camera?.controls ?? []);

// Controls the main panel already owns; showing them twice would invite contradictory edits.
const PRIMARY = new Set([
  "exposure",
  "gain",
  "offset",
  "cooler_on",
  "target_temp",
  "temperature",
  "cooler_power",
]);

// Promoted controls are shown even when the section is collapsed: they change how a capture behaves
// rather than merely how the camera is configured.
const PROMOTED = ["usb_bandwidth", "high_speed"];

const promoted = computed(() =>
  PROMOTED.map((n) => all.value.find((c) => c.name === n && c.writable)).filter(
    (c): c is DeviceControl => !!c,
  ),
);

const rest = computed(() =>
  all.value.filter(
    (c) => c.writable && !PRIMARY.has(c.name) && !PROMOTED.includes(c.name),
  ),
);

// A control the engine has no first-class name for arrives as "x_<whatever the camera called it>".
// It is still shown — the camera says it exists — but labelled from the driver rather than i18n.
function labelFor(c: DeviceControl): string {
  if (c.name.startsWith("x_")) return c.label || c.name.slice(2);
  const key = `capture.adv.${c.name}`;
  const translated = t(key);
  return translated === key ? c.label || c.name : translated;
}

function hintFor(c: DeviceControl): string {
  const key = `capture.adv.${c.name}_hint`;
  const translated = t(key);
  return translated === key ? (c.description ?? "") : translated;
}

// A control whose range is exactly 0..1 is a switch, not a slider.
const isToggle = (c: DeviceControl) => c.min === 0 && c.max === 1;

async function write(c: DeviceControl, value: number) {
  busy.value = c.name;
  try {
    await store.setControl(c.name, value);
  } finally {
    busy.value = "";
  }
}
</script>

<template>
  <div v-if="all.length" class="space-y-2">
    <!-- Promoted: always visible, because they change capture behaviour. -->
    <div v-for="c in promoted" :key="c.name" class="space-y-0.5">
      <label
        class="flex items-center justify-between text-xs text-slate-600 dark:text-slate-300"
      >
        <span>{{ labelFor(c) }}</span>
        <span class="font-mono text-slate-500">
          {{ c.value }}{{ c.unit ? ` ${c.unit}` : "" }}
        </span>
      </label>
      <input
        type="range"
        :min="c.min"
        :max="c.max"
        :value="c.value"
        :disabled="busy === c.name"
        class="w-full accent-brand-600"
        @change="write(c, Number(($event.target as HTMLInputElement).value))"
      />
      <p v-if="hintFor(c)" class="text-[11px] text-slate-400">
        {{ hintFor(c) }}
      </p>
    </div>

    <!-- The long tail, tucked away. -->
    <button
      v-if="rest.length"
      class="text-xs text-brand-600 hover:underline dark:text-brand-300"
      @click="open = !open"
    >
      {{ open ? "▾" : "▸" }}
      {{ t("capture.adv.more", { count: rest.length }) }}
    </button>

    <div
      v-if="open"
      class="space-y-2 border-l border-slate-200 pl-2 dark:border-slate-700"
    >
      <div v-for="c in rest" :key="c.name">
        <!-- 0..1 controls are switches: a slider with two stops is a worse switch. -->
        <label
          v-if="isToggle(c)"
          class="flex items-start gap-1.5 text-xs text-slate-600 dark:text-slate-300"
        >
          <input
            type="checkbox"
            :checked="c.value !== 0"
            :disabled="busy === c.name"
            class="mt-0.5 accent-brand-600"
            @change="
              write(c, ($event.target as HTMLInputElement).checked ? 1 : 0)
            "
          />
          <span>
            {{ labelFor(c) }}
            <span v-if="hintFor(c)" class="block text-[11px] text-slate-400">
              {{ hintFor(c) }}
            </span>
          </span>
        </label>

        <label v-else class="block text-xs text-slate-600 dark:text-slate-300">
          <span class="flex items-center justify-between">
            <span>{{ labelFor(c) }}</span>
            <span class="font-mono text-slate-500">
              {{ c.value }}{{ c.unit ? ` ${c.unit}` : "" }}
            </span>
          </span>
          <input
            type="number"
            :min="c.min"
            :max="c.max"
            :value="c.value"
            :disabled="busy === c.name"
            :class="input"
            class="mt-0.5"
            @change="
              write(c, Number(($event.target as HTMLInputElement).value))
            "
          />
          <span class="text-[11px] text-slate-400">
            {{ t("capture.adv.range", { min: c.min, max: c.max }) }}
          </span>
        </label>
      </div>
    </div>
  </div>
</template>
