<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, input } from "@/constants/styles";
import { FILTERS } from "@/constants/filters";
import { filterHex } from "@/constants/colors";
import { apiGet, apiPost } from "@/services/api";
import { useCaptureStore } from "@/stores/capture";

// What is in each filter wheel slot.
//
// The wheel only knows numbers. This mapping is what turns slot 3 into "Ha" in the FITS header, the
// file name AND the folder name, and the whole pipeline reads those — per-filter flats, channel
// detection, the stacking order. So it is worth entering once and keeping: it is saved server-side,
// not in this browser, because the laptop at the telescope is often not the one that planned the
// session.
//
// Picking from a list rather than typing matters: a slot labelled "Sii " or "sulphur" used to be a
// distinct filter as far as grouping was concerned. The server canonicalizes what it recognises, but
// offering the canonical set up front is what stops the typo happening at all.
const { t } = useI18n();
const store = useCaptureStore();

// names is the editable, wheel-fitted view. savedRaw is EXACTLY what the server holds, kept unfitted.
//
// Keeping the raw copy is what makes a reload work. load() runs on mount, usually before the wheel has
// finished connecting, so the slot count is still 0 and fitting to it yields an empty list — which
// then overwrote the configuration that had just been loaded. Re-deriving from savedRaw when the wheel
// appears restores it instead of losing it.
const names = ref<string[]>([]);
const savedRaw = ref<string[]>([]);
const busy = ref(false);
const error = ref("");

// Slots the user has explicitly switched to free text. A slot whose saved name is not canonical
// (a narrowband filter we do not model, say) counts as custom without being listed here.
const CUSTOM = "__custom__";
const customSlots = ref(new Set<number>());

function isCustom(i: number): boolean {
  const v = names.value[i] ?? "";
  return (
    customSlots.value.has(i) ||
    (v !== "" && !(FILTERS as readonly string[]).includes(v))
  );
}

// The value the <select> shows: the canonical name, "custom…" for anything else, or blank.
function selectValue(i: number): string {
  const v = names.value[i] ?? "";
  if (v === "") return customSlots.value.has(i) ? CUSTOM : "";
  return (FILTERS as readonly string[]).includes(v) ? v : CUSTOM;
}

function pickFilter(i: number, value: string) {
  if (value === CUSTOM) {
    customSlots.value.add(i);
    if (!isCustom(i)) names.value[i] = "";
    return;
  }
  customSlots.value.delete(i);
  names.value[i] = value;
}

// The CONNECTED WHEEL decides how many slots there are — never the saved name list.
//
// `?? names.value.length` used to stand in when no wheel was connected, which meant a 7-filter
// configuration saved for one wheel showed seven rows next to a 5-slot wheel, and offered slots 6
// and 7 that physically do not exist. The names are configuration that outlives any one wheel; the
// count is a fact about the hardware plugged in tonight.
const slots = computed(() => store.wheel?.wheel?.slots ?? 0);

// fitToWheel sizes a name list to the connected wheel: extras dropped, gaps padded. Empty entries are
// preserved — an empty slot 4 is meaningful, and removing it would renumber every filter after it.
function fitToWheel(list: string[]): string[] {
  const n = slots.value;
  if (n <= 0) return [];
  const out = list.slice(0, n);
  while (out.length < n) out.push("");
  return out;
}
const position = computed(() => store.wheel?.wheel?.position ?? 0);
const moving = computed(() => !!store.wheel?.wheel?.moving);
const dirty = computed(
  () =>
    JSON.stringify(names.value) !== JSON.stringify(fitToWheel(savedRaw.value)),
);

onMounted(load);

// Re-fit whenever a wheel connects, disconnects or is swapped for one with a different slot count.
// A wheel connecting, disconnecting or being swapped changes how many slots exist. Re-fit from the
// user's current edits when there are any, otherwise from what was saved — so a wheel that connects
// after the page loaded brings the stored assignment back rather than blanking it.
watch(slots, () => {
  const source = names.value.some((n) => n !== "")
    ? names.value
    : savedRaw.value;
  names.value = fitToWheel(source);
});

// A wheel that connects AFTER the page loaded must still receive the assignment.
//
// Without this the UI showed the saved names while the wheel itself knew none — so every FITS header
// went out with an empty FILTER, the auto-run had no channels to seed from, and the mismatch was
// invisible because the screen looked correct. nextTick lets the slot-count re-fit above land first.
watch(
  () => store.connected.wheel,
  async (connected) => {
    if (!connected) return;
    await nextTick();
    if (names.value.some((n) => n !== "")) await pushNames(names.value);
  },
  { immediate: true },
);

async function load() {
  try {
    const res = await apiGet<{ names: string[] }>("/api/capture/filters");
    savedRaw.value = [...res.names];
    // Fitted for display, so a configuration saved for a bigger wheel cannot show slots this one does
    // not have. The raw copy above survives, so a wheel that connects later re-derives from it.
    names.value = fitToWheel(res.names);
    // Push to a wheel that is already connected, so the mapping takes effect without a save.
    if (store.connected.wheel && res.names.length) await pushNames(res.names);
  } catch {
    // No saved mapping yet is the normal first-run state, not an error worth showing.
  }
}

async function save() {
  busy.value = true;
  error.value = "";
  try {
    const res = await apiPost<{ names: string[] }>("/api/capture/filters", {
      names: names.value,
    });
    savedRaw.value = [...res.names];
    names.value = fitToWheel(res.names);
    if (store.connected.wheel) await pushNames(names.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

// pushNames tells the connected wheel its slot labels without reconnecting it — a filter rename
// mid-session must not drop the USB link or re-home the wheel.
async function pushNames(list: string[]) {
  await apiPost("/api/device/wheel/names", { names: fitToWheel(list) });
  await store.refreshDevices();
}

async function moveTo(slot: number) {
  error.value = "";
  try {
    await store.setFilter(slot);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}
</script>

<template>
  <div class="space-y-2">
    <p class="text-[11px] text-slate-500 dark:text-slate-400">
      {{ t("capture.slots.blurb") }}
    </p>

    <p v-if="!slots" class="text-xs text-slate-400">
      {{ t("capture.slots.noWheel") }}
    </p>

    <div v-else class="space-y-1">
      <div
        v-for="(_, i) in names"
        :key="i"
        class="flex items-center gap-1.5 text-xs"
      >
        <span
          class="w-4 text-right font-mono"
          :class="
            position === i + 1
              ? 'font-semibold text-brand-600 dark:text-brand-300'
              : 'text-slate-400'
          "
          >{{ i + 1 }}</span
        >
        <span
          class="size-2 shrink-0 rounded-full ring-1 ring-black/10 dark:ring-white/15"
          :style="{
            backgroundColor: names[i] ? filterHex(names[i]) : 'transparent',
          }"
          aria-hidden="true"
        />
        <select
          :value="selectValue(i)"
          :class="[input, isCustom(i) ? 'w-20' : 'flex-1']"
          class="!py-0.5"
          :aria-label="t('capture.slots.slotLabel', { n: i + 1 })"
          @change="pickFilter(i, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t("capture.slots.empty") }}</option>
          <option v-for="f in FILTERS" :key="f" :value="f">{{ f }}</option>
          <option :value="CUSTOM">{{ t("capture.slots.custom") }}</option>
        </select>
        <input
          v-if="isCustom(i)"
          v-model="names[i]"
          type="text"
          :placeholder="t('capture.slots.customName')"
          :class="input"
          class="!py-0.5 flex-1"
        />
        <button
          :class="btnGhost"
          class="!px-1.5 !py-0.5 text-[11px]"
          :disabled="!store.connected.wheel || moving || position === i + 1"
          :title="t('capture.slots.goTo')"
          @click="moveTo(i + 1)"
        >
          →
        </button>
      </div>
    </div>

    <div v-if="slots" class="flex items-center gap-2">
      <button
        :class="btnGhost"
        class="!px-2 !py-1 text-xs"
        :disabled="busy || !dirty"
        @click="save"
      >
        {{ dirty ? t("capture.slots.save") : t("capture.slots.saved") }}
      </button>
      <span
        v-if="moving"
        class="text-[11px] text-brand-600 dark:text-brand-300"
      >
        {{ t("capture.slots.moving") }}
      </span>
    </div>

    <p v-if="error" class="text-xs text-danger-500">{{ error }}</p>
  </div>
</template>
