<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, input } from "@/constants/styles";
import { ApiError } from "@/services/api";
import { useEquipmentStore } from "@/stores/equipment";
import { useMosaicStore } from "@/stores/mosaic";
import { useSkyStore } from "@/stores/sky";

// The planner's own rig: pick a saved setup, or edit the optics inline for a one-off combination
// (a reducer swapped in, a different camera) without leaving the page. Tile size — and therefore the
// whole grid — is computed from these numbers, so they get first-class controls here rather than
// being borrowed silently from Tonight.
const { t } = useI18n();
const store = useMosaicStore();
const sky = useSkyStore();
const equipment = useEquipmentStore();

const editing = ref(false);
const saveName = ref("");
const saveError = ref("");

// The optics actually in force: the inline override, else the selected saved setup, else whatever
// the server echoed for the current Tonight rig.
const effective = computed(() => {
  const d = store.draft;
  if (d.optics && Object.values(d.optics).some((v) => v)) return d.optics;
  const setup = equipment.byId(d.setupId);
  if (setup)
    return {
      focal_mm: setup.focal_mm,
      aperture_mm: setup.aperture_mm,
      pixel_um: setup.pixel_um,
      sensor_w_px: setup.sensor_w,
      sensor_h_px: setup.sensor_h,
      barlow_x: setup.barlow,
      reducer_x: setup.reducer,
    };
  const eq = sky.query?.equipment;
  return {
    focal_mm: eq?.focal_mm,
    aperture_mm: eq?.aperture_mm,
    pixel_um: eq?.pixel_um,
    sensor_w_px: eq?.sensor_w_px,
    sensor_h_px: eq?.sensor_h_px,
    barlow_x: eq?.barlow_x,
    reducer_x: eq?.reducer_x,
  };
});

const summary = computed(() => {
  const o = effective.value;
  const parts: string[] = [];
  if (o.focal_mm) parts.push(`${o.focal_mm} mm`);
  if (o.barlow_x && o.barlow_x !== 1) parts.push(`×${o.barlow_x}`);
  if (o.reducer_x && o.reducer_x !== 1) parts.push(`×${o.reducer_x}`);
  if (o.sensor_w_px && o.sensor_h_px)
    parts.push(`${o.sensor_w_px}×${o.sensor_h_px}`);
  if (o.pixel_um) parts.push(`${o.pixel_um} µm`);
  return parts.join(" · ");
});

// Opening the editor seeds it from whatever is currently in force, so the fields never start blank
// and an accidental save can't zero the rig.
function startEditing() {
  store.draft.optics = { ...effective.value };
  editing.value = true;
}

function useSelectedSetup() {
  store.draft.optics = undefined;
  editing.value = false;
}

async function saveAsSetup() {
  saveError.value = "";
  const name = saveName.value.trim();
  if (!name) {
    saveError.value = t("mosaic.setup.nameRequired");
    return;
  }
  const o = effective.value;
  try {
    const id = await equipment.save({
      name,
      focal_mm: o.focal_mm,
      aperture_mm: o.aperture_mm,
      pixel_um: o.pixel_um,
      sensor_w: o.sensor_w_px,
      sensor_h: o.sensor_h_px,
      barlow: o.barlow_x,
      reducer: o.reducer_x,
      eyepieces: [],
    });
    store.draft.setupId = id;
    store.draft.optics = undefined;
    saveName.value = "";
    editing.value = false;
  } catch (e) {
    saveError.value =
      e instanceof ApiError && e.status === 409
        ? t("mosaic.setup.nameExists")
        : String(e instanceof Error ? e.message : e);
  }
}
</script>

<template>
  <div>
    <label
      class="text-sm font-semibold text-slate-700 dark:text-slate-200"
      for="mosaic-setup"
      >{{ t("mosaic.controls.equipment") }}</label
    >
    <select
      id="mosaic-setup"
      v-model="store.draft.setupId"
      :class="input"
      class="mt-1"
      :disabled="editing"
      @change="store.draft.optics = undefined"
    >
      <option value="">{{ t("mosaic.controls.currentSetup") }}</option>
      <option v-for="s in equipment.setups" :key="s.id" :value="s.id">
        {{ s.name }}
      </option>
    </select>

    <p class="mt-1 flex items-center gap-2 text-xs text-slate-400">
      <span>{{ summary || t("mosaic.setup.unknownRig") }}</span>
      <button
        v-if="!editing"
        class="text-brand-600 hover:underline dark:text-brand-300"
        @click="startEditing"
      >
        {{ t("mosaic.setup.edit") }}
      </button>
    </p>

    <div v-if="editing" class="mt-2 space-y-2">
      <div class="grid grid-cols-3 gap-2">
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.focal") }}
          <input
            v-model.number="store.draft.optics!.focal_mm"
            type="number"
            min="0"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.aperture") }}
          <input
            v-model.number="store.draft.optics!.aperture_mm"
            type="number"
            min="0"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.barlow") }}
          <input
            v-model.number="store.draft.optics!.barlow_x"
            type="number"
            min="0"
            step="0.01"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.reducer") }}
          <input
            v-model.number="store.draft.optics!.reducer_x"
            type="number"
            min="0"
            step="0.01"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.pixel") }}
          <input
            v-model.number="store.draft.optics!.pixel_um"
            type="number"
            min="0"
            step="0.01"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.sensorW") }}
          <input
            v-model.number="store.draft.optics!.sensor_w_px"
            type="number"
            min="0"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.setup.sensorH") }}
          <input
            v-model.number="store.draft.optics!.sensor_h_px"
            type="number"
            min="0"
            :class="input"
        /></label>
      </div>
      <p class="text-[11px] text-slate-400">{{ t("mosaic.setup.hint") }}</p>
      <div class="flex flex-wrap items-center gap-2">
        <input
          v-model="saveName"
          :class="input"
          class="flex-1"
          :placeholder="t('mosaic.setup.namePlaceholder')"
        />
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="saveAsSetup"
        >
          {{ t("mosaic.setup.saveAs") }}
        </button>
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="useSelectedSetup"
        >
          {{ t("mosaic.setup.revert") }}
        </button>
      </div>
      <p v-if="saveError" class="text-xs text-danger-500">{{ saveError }}</p>
    </div>
  </div>
</template>
