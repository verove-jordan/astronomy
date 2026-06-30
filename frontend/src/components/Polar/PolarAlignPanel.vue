<script setup lang="ts">
import { onMounted, ref, computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import { usePolarStore } from "@/stores/polar";
import { useSkyStore } from "@/stores/sky";
import { useAutoRefresh } from "@/composables/useAutoRefresh";
import {
  card,
  input,
  segWrap,
  segBtn,
  segActive,
  segIdle,
} from "@/constants/styles";
import { tzForLocation, zonedWallToISO, nowInZone } from "@/utils/tz";

const { t } = useI18n();
const store = usePolarStore();
const sky = useSkyStore();

onMounted(() => store.fetch());
const { enabled: autoRefresh } = useAutoRefresh(() => store.refresh(), 60_000);

const tz = computed(() => {
  const l = store.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});

// Plan for "now" or a specific instant entered in the site's local time (mirrors the Tonight controls).
const useCustomTime = ref(false);
const customLocal = ref("");

function applyTime() {
  if (useCustomTime.value && customLocal.value) {
    store.setTime(zonedWallToISO(customLocal.value, tz.value));
  }
}
watch(useCustomTime, (on) => {
  if (on) {
    autoRefresh.value = false;
    customLocal.value = nowInZone(
      store.query?.at_utc_ms ?? Date.now(),
      tz.value,
    );
    applyTime();
  } else {
    store.setTime(undefined);
  }
});
watch(customLocal, () => applyTime());
watch(tz, () => {
  if (useCustomTime.value) applyTime();
});

// Follow the site chosen on the Targets tab (unless a specific time is pinned).
watch(
  () => sky.query?.location,
  (loc, prev) => {
    if (loc && prev && (loc.lat !== prev.lat || loc.lon !== prev.lon)) {
      store.fetch(undefined, true);
    }
  },
);

const r = computed(() => store.result);

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}
// fmtClock turns a decimal clock hour (0–12) into "h:mm" (12:00 at the top of the dial).
function fmtClock(ch: number): string {
  let h = Math.floor(ch);
  let m = Math.round((ch - h) * 60);
  if (m === 60) {
    m = 0;
    h += 1;
  }
  if (h === 0) h = 12;
  if (h > 12) h -= 12;
  return `${h}:${pad(m)}`;
}
const haHours = computed(() => (r.value ? r.value.ha_deg / 15 : 0));
const clockText = computed(() => fmtClock(store.displayClockHour));
</script>

<template>
  <div :class="card">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("tonight.polar.title") }}
      </h3>
      <div class="flex flex-wrap items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs text-slate-400">
          <input
            v-model="useCustomTime"
            type="checkbox"
            class="accent-brand-500"
          />
          {{ t("tonight.polar.time.specific") }}
        </label>
        <input
          v-if="useCustomTime"
          v-model="customLocal"
          type="datetime-local"
          :class="[input, 'w-auto text-xs']"
        />
        <label v-else class="flex items-center gap-1.5 text-xs text-slate-400">
          <input
            v-model="autoRefresh"
            type="checkbox"
            class="accent-brand-500"
          />
          {{ t("tonight.polar.time.autoRefresh") }}
        </label>
      </div>
    </div>

    <!-- Orientation toggle -->
    <div class="mt-3 flex flex-wrap items-center gap-3">
      <div :class="[segWrap, 'w-max']">
        <button
          type="button"
          :class="[segBtn, store.invert ? segActive : segIdle]"
          @click="store.setInvert(true)"
        >
          {{ t("tonight.polar.orientation.inverting") }}
        </button>
        <button
          type="button"
          :class="[segBtn, !store.invert ? segActive : segIdle]"
          @click="store.setInvert(false)"
        >
          {{ t("tonight.polar.orientation.erect") }}
        </button>
      </div>
      <label class="flex items-center gap-1.5 text-xs text-slate-400">
        <input
          type="checkbox"
          class="accent-brand-500"
          :checked="store.mirror"
          @change="store.setMirror(($event.target as HTMLInputElement).checked)"
        />
        {{ t("tonight.polar.orientation.mirror") }}
      </label>
    </div>
    <p class="mt-1 text-[11px] text-slate-400">
      {{ t("tonight.polar.orientation.hint") }}
    </p>

    <template v-if="r">
      <!-- Headline clock position -->
      <div
        class="mt-3 flex items-baseline gap-2 rounded-md bg-slate-50 px-3 py-2 dark:bg-slate-800/50"
      >
        <span class="text-xs uppercase tracking-wide text-slate-400">{{
          t("tonight.polar.panel.clock")
        }}</span>
        <span
          class="text-2xl font-bold tabular-nums text-brand-600 dark:text-brand-300"
          >{{ clockText }}</span
        >
        <span class="text-xs text-slate-400">· {{ r.pole_star_name }}</span>
      </div>

      <!-- Warnings -->
      <p
        v-if="r.lat_too_low"
        class="mt-2 text-xs text-amber-600 dark:text-amber-400"
      >
        {{ t("tonight.polar.panel.latTooLow") }}
      </p>
      <p
        v-else-if="!r.pole_star_visible"
        class="mt-2 text-xs text-amber-600 dark:text-amber-400"
      >
        {{ t("tonight.polar.panel.notVisible") }}
      </p>

      <!-- Readout grid -->
      <dl
        class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs tabular-nums sm:grid-cols-3"
      >
        <div>
          <dt class="text-slate-400">
            {{ t("tonight.polar.panel.hemisphere") }}
          </dt>
          <dd>{{ t(`tonight.polar.hemisphere.${r.hemisphere}`) }}</dd>
        </div>
        <div>
          <dt class="text-slate-400">
            {{ t("tonight.polar.panel.poleStar") }}
          </dt>
          <dd>{{ r.pole_star_name }}</dd>
        </div>
        <div>
          <dt class="text-slate-400">{{ t("tonight.polar.panel.ha") }}</dt>
          <dd>{{ r.ha_deg.toFixed(1) }}° · {{ haHours.toFixed(1) }}h</dd>
        </div>
        <div>
          <dt class="text-slate-400">
            {{ t("tonight.polar.panel.separation") }}
          </dt>
          <dd>
            {{ r.separation_deg.toFixed(3) }}° ·
            {{ (r.separation_deg * 60).toFixed(0) }}′
          </dd>
        </div>
        <div>
          <dt class="text-slate-400">{{ t("tonight.polar.panel.alt") }}</dt>
          <dd>{{ r.alt_deg.toFixed(1) }}°</dd>
        </div>
        <div>
          <dt class="text-slate-400">{{ t("tonight.polar.panel.az") }}</dt>
          <dd>{{ r.az_deg.toFixed(1) }}°</dd>
        </div>
        <div>
          <dt class="text-slate-400">RA / Dec</dt>
          <dd>
            {{ r.pole_star_ra_deg.toFixed(2) }}° /
            {{ r.pole_star_dec_deg.toFixed(2) }}°
          </dd>
        </div>
        <div>
          <dt class="text-slate-400">{{ t("tonight.polar.panel.lst") }}</dt>
          <dd>{{ r.lst_deg.toFixed(1) }}°</dd>
        </div>
      </dl>
    </template>
    <p v-else-if="store.error" class="mt-3 text-sm text-danger-500">
      {{ store.error }}
    </p>
  </div>
</template>
