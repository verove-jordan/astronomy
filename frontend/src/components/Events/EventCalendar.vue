<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { SkyEvent } from "@/types";
import { fmtDateTime } from "@/utils/tz";
import { kindLabel, kindPillClass } from "@/utils/events";
import EventIcon from "@/components/Events/EventIcon.vue";

// A hand-rolled month grid (native Date + Intl, no dependency). Each day previews its events as small
// coloured type-pills; clicking a day (or a pill) selects it without hiding the rest of the calendar.
const props = defineProps<{
  events: SkyEvent[];
  tz: string;
  month: Date; // any instant within the displayed month
  selectedDay: string | null; // YYYY-MM-DD in the site tz
}>();
const emit = defineEmits<{
  changeMonth: [delta: number];
  selectDay: [day: string | null];
  selectEvent: [id: string];
}>();
const { t, locale } = useI18n();

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}
function dateKey(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
function dayInTz(ms: number): string {
  return fmtDateTime(ms, props.tz).slice(0, 10);
}

const monthLabel = computed(() =>
  new Intl.DateTimeFormat(locale.value, {
    month: "long",
    year: "numeric",
  }).format(props.month),
);

const weekdays = computed(() => {
  const fmt = new Intl.DateTimeFormat(locale.value, {
    weekday: "short",
    timeZone: "UTC",
  });
  return Array.from({ length: 7 }, (_, i) =>
    fmt.format(new Date(Date.UTC(2024, 0, 1 + i))),
  ); // 2024-01-01 is a Monday → Monday-first labels
});

const todayKey = computed(() => dayInTz(Date.now()));

const buckets = computed(() => {
  const m = new Map<string, SkyEvent[]>();
  for (const e of props.events) {
    const k = dayInTz(e.peak_utc_ms);
    (m.get(k) ?? m.set(k, []).get(k)!).push(e);
  }
  for (const list of m.values()) list.sort((a, b) => b.score - a.score);
  return m;
});

interface Cell {
  key: string;
  day: number;
  inMonth: boolean;
  events: SkyEvent[];
}

const cells = computed<Cell[]>(() => {
  const y = props.month.getFullYear();
  const mo = props.month.getMonth();
  const first = new Date(y, mo, 1);
  const offset = (first.getDay() + 6) % 7; // Monday-first column of the 1st
  const start = new Date(y, mo, 1 - offset);
  return Array.from({ length: 42 }, (_, i) => {
    const d = new Date(
      start.getFullYear(),
      start.getMonth(),
      start.getDate() + i,
    );
    const key = dateKey(d);
    return {
      key,
      day: d.getDate(),
      inMonth: d.getMonth() === mo,
      events: buckets.value.get(key) ?? [],
    };
  });
});

function pickDay(c: Cell) {
  if (!c.events.length) return;
  emit("selectDay", props.selectedDay === c.key ? null : c.key); // toggle the day focus
  emit("selectEvent", c.events[0].id); // the day's top-scored event
}

// Clicking a specific event-pill focuses its day (so the list narrows to it) and selects that event.
function pickEvent(c: Cell, e: SkyEvent) {
  emit("selectDay", c.key);
  emit("selectEvent", e.id);
}
</script>

<template>
  <div>
    <div class="mb-2 flex items-center justify-between">
      <button
        type="button"
        class="rounded px-2 py-1 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-700"
        aria-label="previous month"
        @click="emit('changeMonth', -1)"
      >
        ◀
      </button>
      <span class="text-sm font-semibold capitalize">{{ monthLabel }}</span>
      <button
        type="button"
        class="rounded px-2 py-1 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-700"
        aria-label="next month"
        @click="emit('changeMonth', 1)"
      >
        ▶
      </button>
    </div>

    <div class="grid grid-cols-7 gap-px text-center text-[11px] text-slate-400">
      <div v-for="w in weekdays" :key="w" class="py-1 font-medium uppercase">
        {{ w }}
      </div>
    </div>
    <div class="grid grid-cols-7 gap-1">
      <div
        v-for="c in cells"
        :key="c.key"
        :class="[
          'flex min-h-[4.5rem] flex-col gap-0.5 rounded-md border p-1 text-xs',
          c.inMonth
            ? 'border-slate-200 dark:border-slate-700'
            : 'border-transparent text-slate-400 opacity-60',
          c.events.length ? 'cursor-pointer hover:border-brand-400' : '',
          c.key === selectedDay
            ? 'bg-brand-50 ring-2 ring-brand-500 dark:bg-brand-900/30'
            : '',
        ]"
        @click="pickDay(c)"
      >
        <span
          :class="[
            'leading-none',
            c.key === todayKey
              ? 'font-bold text-brand-600 dark:text-brand-300'
              : '',
          ]"
          >{{ c.day }}</span
        >
        <button
          v-for="e in c.events.slice(0, 2)"
          :key="e.id"
          type="button"
          :class="[
            'flex items-center gap-0.5 rounded px-1 py-0.5 text-left text-[9px] font-medium leading-tight',
            kindPillClass(e.kind),
          ]"
          :title="e.title"
          @click.stop="pickEvent(c, e)"
        >
          <EventIcon :kind="e.kind" class="h-2.5 w-2.5 shrink-0" />
          <span class="truncate">{{ kindLabel(e, t) }}</span>
        </button>
        <span
          v-if="c.events.length > 2"
          class="pl-1 text-[9px] text-slate-400"
          >+{{ c.events.length - 2 }}</span
        >
      </div>
    </div>
  </div>
</template>
