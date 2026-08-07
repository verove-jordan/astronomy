<script setup lang="ts">
import { reactive, ref, computed, watch, nextTick, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import { useSkyStore } from "@/stores/sky";
import LocationPicker from "@/components/Sky/LocationPicker.vue";
import ConditionsBadge from "@/components/Sky/ConditionsBadge.vue";
import EyepieceKitTable from "@/components/Sky/EyepieceKitTable.vue";
import IconCamera from "@/components/Icons/IconCamera.vue";
import IconEyepiece from "@/components/Icons/IconEyepiece.vue";
import IconStar from "@/components/Icons/IconStar.vue";
import IconX from "@/components/Icons/IconX.vue";
import {
  card,
  input,
  btnGhost,
  segWrap,
  segBtn,
  segActive,
  segIdle,
  entryBase,
  entrySelected,
} from "@/constants/styles";
import type { SkyEyepiece, LocationFavorite, EquipmentSetup } from "@/types";
import { bortleColor } from "@/utils/bortle";
import { effectiveFocalMm } from "@/utils/optics";

const { t } = useI18n();
const store = useSkyStore();

type Mode = "camera" | "visual";

interface ControlForm {
  lat: string;
  lon: string;
  elevation_m: string;
  focal_mm: string;
  aperture_mm: string;
  pixel_um: string;
  sensor_w: string;
  sensor_h: string;
  barlow: string;
  reducer: string;
  min_alt: string;
  twilight: "astro" | "nautical";
  mode: Mode;
}

const form = reactive<ControlForm>({
  lat: "",
  lon: "",
  elevation_m: "",
  focal_mm: "",
  aperture_mm: "",
  pixel_um: "",
  sensor_w: "",
  sensor_h: "",
  barlow: "",
  reducer: "",
  min_alt: "30",
  twilight: "astro",
  mode: "camera",
});

// Local, editable copy of the visual kit (seeded once from the store); edits commit to the store.
const epKit = ref<SkyEyepiece[]>(store.eyepieceKit.map((e) => ({ ...e })));

// The human label of the current site (set when picked from address search; "" for map-click/geolocation).
// Used as the default name when saving a favorite.
const currentLabel = ref("");

// Auto-apply: any setup change re-scores automatically (debounced), so there is no Apply button.
// `seeding` suppresses the watcher while we copy server values back into the form.
let seeding = false;
let applyTimer: number | undefined;
function scheduleApply() {
  if (seeding) return;
  window.clearTimeout(applyTimer);
  applyTimer = window.setTimeout(() => apply(), 400);
}
onBeforeUnmount(() => window.clearTimeout(applyTimer));

function seedFromEcho() {
  const q = store.query;
  if (!q) return;
  seeding = true;
  form.lat = String(q.location.lat);
  form.lon = String(q.location.lon);
  form.elevation_m = String(q.location.elevation_m);
  form.focal_mm = String(q.equipment.focal_mm);
  form.aperture_mm = String(q.equipment.aperture_mm);
  form.pixel_um = String(q.equipment.pixel_um);
  form.sensor_w = String(q.equipment.sensor_w_px);
  form.sensor_h = String(q.equipment.sensor_h_px);
  // Show a multiplier only when one is actually fitted (×1 reads as "none"). The two are separate
  // fields because a reducer usually stays in the train while a Barlow is swapped in — and a Barlow is
  // by definition ≥1, so its guard stays `> 1` while the reducer's only has to exclude ×1.
  form.barlow = q.equipment.barlow_x > 1 ? String(q.equipment.barlow_x) : "";
  form.reducer =
    q.equipment.reducer_x > 0 && q.equipment.reducer_x !== 1
      ? String(q.equipment.reducer_x)
      : "";
  form.min_alt = String(q.min_alt_deg);
  form.twilight = q.twilight === "nautical" ? "nautical" : "astro";
  form.mode = q.equipment.mode === "visual" ? "visual" : "camera";
  void nextTick(() => {
    seeding = false;
  });
}

// Mode toggle: switch the planner immediately (snappy), sending the kit when going visual.
function setMode(mode: Mode) {
  if (form.mode === mode) return;
  form.mode = mode;
  store.setMode(mode);
}

// The table owns add/remove/edit and hands back a fresh array; we adopt it and re-commit.
function setKit(next: SkyEyepiece[]) {
  epKit.value = next;
  commitKit();
}

// commitKit pushes the valid rows to the store (which re-scores when in visual mode).
function commitKit() {
  store.setEyepieceKit(
    epKit.value
      .filter((e) => e.focal_mm > 0 && e.afov_deg > 0)
      .map((e) => ({
        label: e.label.trim() || `${e.focal_mm}mm`,
        focal_mm: e.focal_mm,
        afov_deg: e.afov_deg,
      })),
  );
}

// Seed the form once the first server echo arrives.
watch(
  () => store.query,
  (q, prev) => {
    if (q && !prev) seedFromEcho();
  },
  { immediate: true },
);

// Re-score automatically whenever any setup field changes (debounced). Mode + eyepiece-kit edits have
// their own immediate handlers; location map-clicks call apply() directly.
watch(
  () => [
    form.lat,
    form.lon,
    form.elevation_m,
    form.focal_mm,
    form.aperture_mm,
    form.barlow,
    form.reducer,
    form.pixel_um,
    form.sensor_w,
    form.sensor_h,
    form.min_alt,
    form.twilight,
  ],
  scheduleApply,
);

const fov = computed(() => {
  const e = store.query?.equipment;
  if (!e) return "";
  const barlow = e.barlow_x > 1 ? ` · ${e.barlow_x}× Barlow` : "";
  const reducer =
    e.reducer_x > 0 && e.reducer_x !== 1
      ? ` · ${e.reducer_x}× ${t("tonight.equipment.reducerShort")}`
      : "";
  return `${e.fov_w_deg.toFixed(2)}° × ${e.fov_h_deg.toFixed(2)}° · ${e.image_scale_arcsec_px.toFixed(2)}″/px · f/${e.f_ratio.toFixed(1)}${barlow}${reducer}`;
});

function num(v: string): number | undefined {
  const n = parseFloat(v);
  return Number.isFinite(n) ? n : undefined;
}

// The eyepiece table has to react to a focal/Barlow/reducer edit IMMEDIATELY, so it reads the form
// rather than the (debounced, round-tripped) server echo — falling back to the echo before the first
// edit. The wide "Champ … ″/px … f/…" line stays server-derived, as it always was.
const liveFocalMm = computed(
  () => num(form.focal_mm) ?? store.query?.equipment.focal_mm ?? 0,
);
const liveApertureMm = computed(
  () => num(form.aperture_mm) ?? store.query?.equipment.aperture_mm ?? 0,
);
const liveEffectiveFocalMm = computed(() =>
  effectiveFocalMm(liveFocalMm.value, num(form.barlow), num(form.reducer)),
);

// "740 mm × 0,66 = 488 mm" — shown whenever a multiplier actually changes the focal length.
const effectiveFocalLabel = computed(() => {
  const mults = [num(form.barlow), num(form.reducer)].filter(
    (m): m is number => m !== undefined && m > 0 && m !== 1,
  );
  if (!mults.length || liveFocalMm.value <= 0) return "";
  const chain = mults.map((m) => ` × ${m}`).join("");
  return `${liveFocalMm.value} mm${chain} = ${Math.round(liveEffectiveFocalMm.value)} mm`;
});

// Coordinates fed to the map/marker — current form value, else the server echo, else Paris.
const pickerLat = computed(
  () => num(form.lat) ?? store.query?.location.lat ?? 48.8566,
);
const pickerLon = computed(
  () => num(form.lon) ?? store.query?.location.lon ?? 2.3522,
);

function apply() {
  store.fetch(
    {
      lat: num(form.lat),
      lon: num(form.lon),
      elevation_m: num(form.elevation_m),
      focal_mm: num(form.focal_mm),
      aperture_mm: num(form.aperture_mm),
      pixel_um: num(form.pixel_um),
      sensor_w: num(form.sensor_w),
      sensor_h: num(form.sensor_h),
      barlow: num(form.barlow),
      reducer: num(form.reducer),
      min_alt: num(form.min_alt),
      twilight: form.twilight,
      mode: form.mode,
    },
    false, // auto-apply: refetch only when params actually changed (cache no-ops otherwise)
  );
}

async function reset() {
  await store.reset();
  seedFromEcho();
}

// setLatLon is called by the location picker (map click / address search), favorites, and the dark-sky
// finder — it fills, records the place label (if any), and re-scores.
function setLatLon(lat: number, lon: number, label?: string) {
  form.lat = lat.toFixed(5);
  form.lon = lon.toFixed(5);
  currentLabel.value = label ?? "";
  apply();
}

function useMyLocation() {
  if (!navigator.geolocation) return;
  navigator.geolocation.getCurrentPosition((pos) =>
    setLatLon(pos.coords.latitude, pos.coords.longitude),
  );
}

// --- Favorite observing sites ---------------------------------------------------------------------
// Whether the current site is saved, and the id of its chip (to highlight it). The key formula lives in
// the store, so we reuse it rather than re-deriving the rounding here.
const currentFavId = computed(() =>
  store.favLocKey(pickerLat.value, pickerLon.value),
);
const isCurrentFavorite = computed(() =>
  store.isLocationFavorite(pickerLat.value, pickerLon.value),
);

// Save (or unsave) the current site — default label is the searched place name, else a "lat, lon" string.
function toggleCurrentFavorite() {
  store.toggleLocationFavorite({
    label:
      currentLabel.value ||
      `${pickerLat.value.toFixed(3)}, ${pickerLon.value.toFixed(3)}`,
    lat: pickerLat.value,
    lon: pickerLon.value,
    elevation_m: num(form.elevation_m),
  });
}

// Jump to a saved favorite: restore its elevation, then re-score for its coordinates.
function applyFavorite(fav: LocationFavorite) {
  if (fav.elevation_m != null) form.elevation_m = String(fav.elevation_m);
  setLatLon(fav.lat, fav.lon, fav.label);
}

// Inline rename of a favorite chip (double-click → edit; Enter/blur commits, Esc cancels).
const editingFavId = ref<string | null>(null);
const favDraft = ref("");
function startRenameFav(fav: LocationFavorite) {
  editingFavId.value = fav.id;
  favDraft.value = fav.label;
}
function commitRenameFav() {
  if (editingFavId.value) {
    const label = favDraft.value.trim();
    if (label) store.renameLocationFavorite(editingFavId.value, label);
  }
  editingFavId.value = null;
}

// --- Equipment setups (named telescope + camera + eyepiece rigs) -----------------------------------
const setupName = ref("");

// Save the current gear (telescope + camera + eyepieces) under a name for one-click reuse later.
function saveCurrentSetup() {
  if (!setupName.value.trim()) return;
  store.saveEquipmentSetup({
    name: setupName.value,
    focal_mm: num(form.focal_mm),
    aperture_mm: num(form.aperture_mm),
    barlow: num(form.barlow),
    reducer: num(form.reducer),
    pixel_um: num(form.pixel_um),
    sensor_w: num(form.sensor_w),
    sensor_h: num(form.sensor_h),
    eyepieces: epKit.value
      .filter((e) => e.focal_mm > 0 && e.afov_deg > 0)
      .map((e) => ({ ...e })),
  });
  setupName.value = "";
}

// Apply a saved setup: fill the gear fields (+ eyepiece kit if the setup has one), then re-score. The
// camera/eyepiece mode is left as-is.
function applySetup(s: EquipmentSetup) {
  const str = (v?: number) => (v != null ? String(v) : "");
  form.focal_mm = str(s.focal_mm);
  form.aperture_mm = str(s.aperture_mm);
  form.barlow = s.barlow != null && s.barlow > 1 ? String(s.barlow) : "";
  form.reducer =
    s.reducer != null && s.reducer > 0 && s.reducer !== 1
      ? String(s.reducer)
      : "";
  form.pixel_um = str(s.pixel_um);
  form.sensor_w = str(s.sensor_w);
  form.sensor_h = str(s.sensor_h);
  if (s.eyepieces.length) {
    epKit.value = s.eyepieces.map((e) => ({ ...e }));
    commitKit();
  }
  apply();
}

// Inline rename of a setup chip (double-click → edit; Enter/blur commits, Esc cancels).
const editingSetupId = ref<string | null>(null);
const setupDraft = ref("");
function startRenameSetup(s: EquipmentSetup) {
  editingSetupId.value = s.id;
  setupDraft.value = s.name;
}
function commitRenameSetup() {
  if (editingSetupId.value)
    store.renameEquipmentSetup(editingSetupId.value, setupDraft.value);
  editingSetupId.value = null;
}

defineExpose({ setLatLon });
</script>

<template>
  <div :class="card">
    <div class="grid gap-4 lg:grid-cols-[28rem_minmax(0,1fr)] lg:items-start">
      <!-- Map: address search + big square map + overlay toggles + legend -->
      <LocationPicker :lat="pickerLat" :lon="pickerLon" @pick="setLatLon" />

      <!-- Setup controls, beside the map -->
      <div class="space-y-4">
        <!-- Location -->
        <fieldset class="min-w-0">
          <legend
            class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
          >
            {{ t("tonight.location.title") }}
          </legend>
          <div class="grid grid-cols-2 gap-2">
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.location.lat") }}
              <input
                v-model="form.lat"
                :class="input"
                type="number"
                step="0.0001"
              />
            </label>
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.location.lon") }}
              <input
                v-model="form.lon"
                :class="input"
                type="number"
                step="0.0001"
              />
            </label>
          </div>
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <div
              v-if="store.site"
              class="flex items-center gap-2 rounded-md border border-slate-200 px-2 py-1 text-xs dark:border-slate-700"
              :title="t(`tonight.site.source.${store.site.source}`)"
            >
              <span
                class="inline-block h-3 w-3 shrink-0 rounded-full border border-slate-300 dark:border-slate-600"
                :style="{ backgroundColor: bortleColor(store.site.bortle) }"
              />
              <span class="text-slate-600 dark:text-slate-300">
                {{
                  t("tonight.site.label", {
                    bortle: store.site.bortle,
                    sqm: store.site.sqm.toFixed(1),
                  })
                }}
              </span>
            </div>
            <ConditionsBadge />
            <button
              type="button"
              class="ml-auto"
              :class="
                isCurrentFavorite
                  ? 'text-amber-400'
                  : 'text-slate-400 hover:text-amber-400'
              "
              :aria-label="
                isCurrentFavorite
                  ? t('tonight.location.favorites.remove')
                  : t('tonight.location.favorites.save')
              "
              :title="
                isCurrentFavorite
                  ? t('tonight.location.favorites.remove')
                  : t('tonight.location.favorites.save')
              "
              @click="toggleCurrentFavorite"
            >
              <IconStar :filled="isCurrentFavorite" />
            </button>
            <button :class="btnGhost" type="button" @click="useMyLocation">
              {{ t("tonight.location.useMyLocation") }}
            </button>
          </div>

          <!-- Saved sites: click a chip to jump, ✕ to remove, double-click to rename -->
          <div
            v-if="store.locationFavorites.length"
            class="mt-2 flex flex-wrap items-center gap-1.5"
          >
            <span
              class="text-[10px] font-semibold uppercase tracking-wide text-slate-400"
            >
              {{ t("tonight.location.favorites.title") }}
            </span>
            <template v-for="fav in store.locationFavorites" :key="fav.id">
              <span v-if="editingFavId === fav.id" :class="entrySelected">
                <input
                  v-model="favDraft"
                  class="w-28 bg-transparent text-xs outline-none"
                  :aria-label="t('tonight.location.favorites.namePlaceholder')"
                  :placeholder="t('tonight.location.favorites.namePlaceholder')"
                  @keyup.enter="commitRenameFav"
                  @keyup.esc="editingFavId = null"
                  @blur="commitRenameFav"
                />
              </span>
              <span
                v-else
                :class="fav.id === currentFavId ? entrySelected : entryBase"
              >
                <button
                  type="button"
                  class="inline-flex items-center gap-1"
                  :title="`${fav.label} · ${fav.lat.toFixed(4)}, ${fav.lon.toFixed(4)} — ${t('tonight.location.favorites.renameHint')}`"
                  @click="applyFavorite(fav)"
                  @dblclick="startRenameFav(fav)"
                >
                  <IconStar class="text-amber-400" :filled="true" />
                  <span class="max-w-[10rem] truncate">{{ fav.label }}</span>
                </button>
                <button
                  type="button"
                  class="-mr-0.5 text-slate-400 hover:text-danger-500"
                  :aria-label="t('common.remove')"
                  @click="store.removeLocationFavorite(fav.id)"
                >
                  <IconX class="h-3.5 w-3.5" />
                </button>
              </span>
            </template>
          </div>
        </fieldset>

        <!-- Equipment -->
        <fieldset class="min-w-0">
          <legend
            class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
          >
            {{ t("tonight.equipment.title") }}
          </legend>

          <!-- Saved rigs: click a chip to load it, ✕ to remove, double-click to rename -->
          <div class="mb-2 flex flex-wrap items-center gap-1.5">
            <span
              class="text-[10px] font-semibold uppercase tracking-wide text-slate-400"
            >
              {{ t("tonight.equipment.setups.title") }}
            </span>
            <template v-for="s in store.equipmentSetups" :key="s.id">
              <span v-if="editingSetupId === s.id" :class="entrySelected">
                <input
                  v-model="setupDraft"
                  class="w-28 bg-transparent text-xs outline-none"
                  :aria-label="t('tonight.equipment.setups.namePlaceholder')"
                  :placeholder="t('tonight.equipment.setups.namePlaceholder')"
                  @keyup.enter="commitRenameSetup"
                  @keyup.esc="editingSetupId = null"
                  @blur="commitRenameSetup"
                />
              </span>
              <span v-else :class="entryBase">
                <button
                  type="button"
                  class="inline-flex items-center gap-1"
                  :title="t('tonight.equipment.setups.renameHint')"
                  @click="applySetup(s)"
                  @dblclick="startRenameSetup(s)"
                >
                  <IconEyepiece class="h-3 w-3 text-slate-400" />
                  <span class="max-w-[10rem] truncate">{{ s.name }}</span>
                </button>
                <button
                  type="button"
                  class="-mr-0.5 text-slate-400 hover:text-danger-500"
                  :aria-label="t('common.remove')"
                  @click="store.removeEquipmentSetup(s.id)"
                >
                  <IconX class="h-3.5 w-3.5" />
                </button>
              </span>
            </template>
            <!-- Save the current gear as a new named rig (or overwrite one of the same name) -->
            <span class="inline-flex items-center gap-1">
              <input
                v-model="setupName"
                :class="input"
                class="!w-32 !py-1 !text-xs"
                :placeholder="t('tonight.equipment.setups.namePlaceholder')"
                @keyup.enter="saveCurrentSetup"
              />
              <button
                :class="btnGhost"
                class="!px-2 !py-1 !text-xs"
                :disabled="!setupName.trim()"
                @click="saveCurrentSetup"
              >
                {{ t("tonight.equipment.setups.save") }}
              </button>
            </span>
          </div>

          <!-- Camera vs eyepiece mode -->
          <div :class="[segWrap, 'mb-2 w-max']">
            <button
              type="button"
              :class="[
                segBtn,
                'flex items-center gap-1',
                form.mode === 'camera' ? segActive : segIdle,
              ]"
              @click="setMode('camera')"
            >
              <IconCamera class="h-3 w-3" /> {{ t("tonight.mode.camera") }}
            </button>
            <button
              type="button"
              :class="[
                segBtn,
                'flex items-center gap-1',
                form.mode === 'visual' ? segActive : segIdle,
              ]"
              @click="setMode('visual')"
            >
              <IconEyepiece class="h-3 w-3" /> {{ t("tonight.mode.eyepiece") }}
            </button>
          </div>

          <div class="grid grid-cols-2 gap-2">
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.equipment.focalLength") }}
              <input
                v-model="form.focal_mm"
                :class="input"
                type="number"
                step="1"
              />
            </label>
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.equipment.aperture") }}
              <input
                v-model="form.aperture_mm"
                :class="input"
                type="number"
                step="1"
              />
            </label>
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.equipment.barlow") }}
              <input
                v-model="form.barlow"
                :class="input"
                type="number"
                step="0.1"
                min="1"
                :placeholder="t('tonight.equipment.barlowNone')"
              />
            </label>
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.equipment.reducer") }}
              <input
                v-model="form.reducer"
                :class="input"
                type="number"
                step="0.01"
                min="0.1"
                max="1"
                :placeholder="t('tonight.equipment.reducerNone')"
              />
            </label>
            <template v-if="form.mode === 'camera'">
              <label class="text-xs text-slate-500 dark:text-slate-400">
                {{ t("tonight.equipment.pixelSize") }}
                <input
                  v-model="form.pixel_um"
                  :class="input"
                  type="number"
                  step="0.01"
                />
              </label>
              <label class="text-xs text-slate-500 dark:text-slate-400">
                {{ t("tonight.equipment.sensorWidth") }}
                <input
                  v-model="form.sensor_w"
                  :class="input"
                  type="number"
                  step="1"
                />
              </label>
              <label class="text-xs text-slate-500 dark:text-slate-400">
                {{ t("tonight.equipment.sensorHeight") }}
                <input
                  v-model="form.sensor_h"
                  :class="input"
                  type="number"
                  step="1"
                />
              </label>
            </template>
          </div>

          <!-- What the multipliers actually make of the focal length, live as you type -->
          <p
            v-if="effectiveFocalLabel"
            class="mt-1 text-xs text-slate-500 dark:text-slate-400"
          >
            {{ t("tonight.equipment.effectiveFocal") }}:
            <span class="font-medium text-slate-700 dark:text-slate-200">{{
              effectiveFocalLabel
            }}</span>
          </p>

          <!-- Eyepiece kit editor (visual mode) -->
          <EyepieceKitTable
            v-if="form.mode === 'visual'"
            :model-value="epKit"
            :effective-focal-mm="liveEffectiveFocalMm"
            :aperture-mm="liveApertureMm"
            @update:model-value="setKit"
          />

          <p
            v-if="fov && form.mode === 'camera'"
            class="mt-1 truncate text-xs text-slate-400"
            :title="fov"
          >
            {{ t("tonight.equipment.fov") }}: {{ fov }}
          </p>
        </fieldset>

        <!-- Darkness + apply -->
        <fieldset class="min-w-0">
          <legend
            class="mb-1 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
          >
            {{ t("tonight.controls.twilight") }}
          </legend>
          <div class="grid grid-cols-2 gap-2">
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.controls.minAlt") }}
              <input
                v-model="form.min_alt"
                :class="input"
                type="number"
                step="1"
              />
            </label>
            <label class="text-xs text-slate-500 dark:text-slate-400">
              {{ t("tonight.controls.twilight") }}
              <select v-model="form.twilight" :class="input">
                <option value="astro">
                  {{ t("tonight.twilightKind.astro") }}
                </option>
                <option value="nautical">
                  {{ t("tonight.twilightKind.nautical") }}
                </option>
              </select>
            </label>
          </div>
          <div class="mt-2">
            <button :class="[btnGhost, 'w-full']" type="button" @click="reset">
              {{ t("tonight.controls.reset") }}
            </button>
            <p class="mt-1 text-[10px] text-slate-400">
              {{ t("tonight.controls.autoApply") }}
            </p>
          </div>
        </fieldset>
      </div>
    </div>
  </div>
</template>
