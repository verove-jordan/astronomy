<script setup lang="ts">
import { computed, ref, watch } from "vue";

// A duration stored in MICROSECONDS, typed in a unit the user picks.
//
// Two things it exists to get right, both learned from the same broken field:
//
//   - It OWNS ITS TEXT while focused. Binding :value to a value derived from the model looks
//     equivalent and is not: the input is then rebuilt from the model on every render of the parent,
//     so a half-typed "0." comes straight back as "0" and the decimal point can never be entered at
//     all. In isolation it never shows, because the value only round-trips when it changed — but
//     during a run the progress stream re-renders the panel once a second, which is exactly when
//     somebody wants to shorten an exposure.
//   - It OFFERS THE UNIT. A bias frame is 32 µs. Asking for that in a seconds box means typing
//     0.000032 and counting the zeros, which is not a thing to ask of anyone at 2am. The live camera
//     panel already works this way; the sequence rows now match it.

const units = [
  { key: "us", factor: 1, label: "µs" },
  { key: "ms", factor: 1_000, label: "ms" },
  { key: "s", factor: 1_000_000, label: "s" },
] as const;
type Unit = (typeof units)[number];

const props = withDefaults(
  defineProps<{
    modelValue: number;
    /** Smallest value that may be stored, in microseconds. A zero-length exposure is refused by the
     * sequencer ("exposure must be positive"), so the field refuses it too rather than letting a run
     * be rejected at the moment somebody presses Start. */
    minUs?: number;
    inputClass?: string;
    selectClass?: string;
  }>(),
  { minUs: 1, inputClass: "", selectClass: "" },
);
const emit = defineEmits<{ "update:modelValue": [number] }>();

// fits picks the unit a duration reads most naturally in, on the same thresholds the camera panel
// uses, so the two never disagree about what 1500 µs is called.
function fits(us: number): Unit {
  if (us >= 1_000_000) return units[2];
  if (us >= 1_000) return units[1];
  return units[0];
}

// render is the shortest exact text for a duration in a unit. Six decimals is one microsecond, so
// nothing is ever lost, and trailing zeros are dropped: 60 s is "60", not "60.000000".
function render(us: number, unit: Unit): string {
  if (!Number.isFinite(us) || us <= 0) return "";
  return String(Number((us / unit.factor).toFixed(6)));
}

const unit = ref<Unit>(fits(props.modelValue));
const text = ref(render(props.modelValue, unit.value));
const focused = ref(false);

watch(
  () => props.modelValue,
  (us) => {
    // A change from elsewhere — a bulk edit, a loaded sequence — is worth showing. One the user is
    // half-way through typing is not.
    if (focused.value) return;
    unit.value = fits(us);
    text.value = render(us, unit.value);
  },
);

function commit(seconds: number) {
  emit("update:modelValue", Math.max(props.minUs, Math.round(seconds)));
}

function onInput(event: Event) {
  text.value = (event.target as HTMLInputElement).value;
  const typed = Number(text.value);
  // An empty or half-formed entry ("", "0.", "-") leaves the stored value alone rather than
  // committing a number the user has not finished writing.
  if (text.value.trim() === "" || !Number.isFinite(typed)) return;
  commit(typed * unit.value.factor);
}

// Changing the unit RE-EXPRESSES the duration, it does not reinterpret the number: 60 s becomes
// 60000 ms, never 60 ms. The camera panel does the opposite, and there it is harmless because the
// value is applied by hand and visible. Here it would silently turn a 60-second sub into a
// 60-millisecond one, and nobody would find out until the stack came back empty.
function onUnitChange(event: Event) {
  const chosen = units.find(
    (u) => u.key === (event.target as HTMLSelectElement).value,
  );
  if (!chosen) return;
  unit.value = chosen;
  text.value = render(props.modelValue, chosen);
}

function onBlur() {
  focused.value = false;
  // Show what was actually stored, including any clamp, so the field never disagrees with the run.
  unit.value = fits(props.modelValue);
  text.value = render(props.modelValue, unit.value);
}

const minInUnit = computed(() => props.minUs / unit.value.factor);
</script>

<template>
  <span class="inline-flex items-center gap-1">
    <input
      :value="text"
      type="number"
      :min="minInUnit"
      step="any"
      inputmode="decimal"
      :class="inputClass"
      @focus="focused = true"
      @input="onInput"
      @blur="onBlur"
    />
    <select
      :value="unit.key"
      :class="selectClass"
      aria-label="unit"
      @change="onUnitChange"
    >
      <option v-for="u in units" :key="u.key" :value="u.key">
        {{ u.label }}
      </option>
    </select>
  </span>
</template>
