<script setup lang="ts">
import { ref, computed, onMounted, nextTick, watch } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useBrowseStore } from "@/stores/browse";
import { useJobsStore } from "@/stores/jobs";
import { useS3Store, type TransferOp } from "@/stores/s3";
import { usePresetsStore } from "@/stores/presets";
import { useMosaicStore } from "@/stores/mosaic";
import { useCaptureSummary } from "@/composables/useCaptureSummary";
import { useChannelMapping } from "@/composables/useChannelMapping";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import FileBrowser from "@/components/Common/FileBrowser.vue";
import Spinner from "@/components/Common/Spinner.vue";
import CaptureSummary from "@/components/Capture/CaptureSummary.vue";
import FilterMappingEditor from "@/components/Capture/FilterMappingEditor.vue";
import ReusePanel from "@/components/Capture/ReusePanel.vue";
import CalibrationPanel from "@/components/Capture/CalibrationPanel.vue";
import SetArtifactsModal from "@/components/Capture/SetArtifactsModal.vue";
import SessionBreakdown from "@/components/Capture/SessionBreakdown.vue";
import FilePreviewButton from "@/components/Common/FilePreviewButton.vue";
import ParamGlossary from "@/components/Common/ParamGlossary.vue";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import TwoPane from "@/components/Common/TwoPane.vue";
import StatusPill from "@/components/Common/StatusPill.vue";
import EnvWarnings from "@/components/Common/EnvWarnings.vue";
import IconFolder from "@/components/Icons/IconFolder.vue";
import IconCloud from "@/components/Icons/IconCloud.vue";
import type { CreateOpts, KnobRange } from "@/stores/jobs";
import type {
  AlignPointsEstimate,
  ReusePreview,
  CalibPreview,
  RunPlanPreview,
  ProcessingHistoryEntry,
  PresetItem,
  PresetPayload,
  SetQaReport,
} from "@/types";
import {
  btnPrimary,
  btnGhost,
  card,
  input,
  checkbox,
  frameTypeAccentClass,
  frameTypeCardClass,
} from "@/constants/styles";
import { humanizeMs, baseName, formatTimestamp } from "@/utils/format";
import { nudged, oppositeOf } from "@/utils/params";
import type { FrameSet } from "@/types";

const router = useRouter();
const { t } = useI18n();
const browseStore = useBrowseStore();
const jobsStore = useJobsStore();
const s3 = useS3Store();
// Storage mode for a run (only offered when S3 is active): "local" keeps files on disk; "s3" pulls inputs
// from S3, processes locally, pushes inputs+results back to S3, then frees the local copies (verified).
const processMode = ref<"local" | "s3">("local");
const lowDisk = ref(true); // staged low-disk S3 processing (default on; deep-sky/nebula only)

// Import file-source tab: browse local disk vs the S3 mirror. Both drive the same FileBrowser over the
// DataDir tree, filtered by source; the selection is shared across tabs. S3-only folders download to local
// before inspect (downloadingS3 = count in flight; inspectError surfaces a failure).
const sourceTab = ref<"local" | "s3">("local");
const tabClass = (kind: "local" | "s3") =>
  kind === sourceTab.value
    ? "rounded-md px-3 py-2 text-sm font-medium bg-brand-600 text-white"
    : "rounded-md px-3 py-2 text-sm font-medium text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700";
// S3 → local progress feedback: downloadingS3 = folders being pulled, downloadedS3 = how many finished,
// inspecting = the post-download frame scan. s3Busy drives the browser's disabled/busy button + banner.
const downloadingS3 = ref(0);
const downloadedS3 = ref(0);
const inspecting = ref(false);
const inspectError = ref("");
const s3Busy = computed(() => downloadingS3.value > 0 || inspecting.value);
const s3BusyLabel = computed(() =>
  inspecting.value ? t("import.inspectingBtn") : t("import.downloadingBtn"),
);

const selectedPaths = ref<string[]>([]);
const rootPath = ref("");
const selectedMode = ref("deepsky");
const selectedFormat = ref("image");
const launching = ref(false);

// Run-quality toggles (defaults match the backend presets).
const colorCalibration = ref(true);
const denoise = ref(true);
const dropWheelTransition = ref(true);
const haExcludeStars = ref(true); // default: Hα on the galaxy/nebulosity only; uncheck → over everything
const mosaic = ref(false); // multi-night union canvas (keep every night's full field) — consent knob, never preset-enabled
// Extra monochrome side-outputs (deepsky/nebula), saved next to the colour final: a processed
// Luminance-only image (default on) and a combined all-channel integration (default off).
const outputLuminance = ref(true);
const outputMonoStack = ref(false);
const earthshine = ref(false); // planetary: reveal the Moon's unlit side (earthshine) on the final render
// Planetary align-points estimator: min detail size (px; null = auto), busy flag, last result/error.
const alignPointsMinPx = ref<number | null>(null);
const alignPointsBusy = ref(false);
const alignPointsResult = ref<AlignPointsEstimate | null>(null);
const alignPointsError = ref("");
// Opt-in: drive the local AI agent to auto-tune the finish (every stacking mode). Off by default.
const supervise = ref(false);

const modes = ["deepsky", "nebula", "milkyway", "planetary", "comet", "mosaic"];
const formats = ["image", "video", "both"];

// Milky-Way nightscape render style (foreground composite + linear grade); only shown for milkyway.
const look = ref("natural");
const looks = ["natural", "iphone", "deepsky"];
const palette = ref("natural");
// Deep-sky colour palettes + the filters each needs. Narrowband palettes are shown but disabled until
// their OIII/SII data exists (the engine also soft-falls back); natural/mono always apply.
const paletteOptions: { value: string; needs: string[] }[] = [
  { value: "natural", needs: [] },
  { value: "hargb", needs: ["Ha"] },
  { value: "hoo", needs: ["Ha", "OIII"] },
  { value: "sho", needs: ["SII", "Ha", "OIII"] },
  { value: "hos", needs: ["SII", "Ha", "OIII"] },
  { value: "foraxx", needs: ["Ha", "OIII"] },
  { value: "mono", needs: [] },
];
// Sky brightness target for the nightscape auto-levels (data-driven stretch); balanced is the default.
const brightness = ref("balanced");
const brightnesses = ["darker", "balanced", "brighter"];
// Final display orientation. The default ("" = EXIF, the backend default — sent as nothing) matches
// the photo's real EXIF orientation; "auto" opts into the legacy content heuristic (bright-half =
// sky); the explicit overrides are for the rare frame that still comes out wrong. "Mirror" appends a
// horizontal flip (explicit overrides only). orientationValue folds the two into the backend token.
const orientation = ref("");
const orientations = [
  { value: "", label: "exif" },
  { value: "auto", label: "auto" },
  { value: "none", label: "none" },
  { value: "cw", label: "cw" },
  { value: "ccw", label: "ccw" },
  { value: "180", label: "180" },
];
const mirror = ref(false);
const orientationValue = computed(() => {
  const base = orientation.value;
  if (base === "" || base === "auto") return base; // exif/content — no mirror variant
  if (mirror.value) return base === "none" ? "flip" : base + "-flip";
  return base;
});
// Optional calibration-frame folders (dark/flat/bias) applied before stacking; empty = none.
const darkDir = ref("");
const flatDir = ref("");
const biasDir = ref("");
const isMilkyway = computed(() => selectedMode.value === "milkyway");
const isPlanetary = computed(() => selectedMode.value === "planetary");
// Tiled-panel mosaic mode: offers the saved-plan selector (panel labeling/validation + solve hints).
const isMosaicMode = computed(() => selectedMode.value === "mosaic");
const mosaicStore = useMosaicStore();
const mosaicPlanId = ref(0);
watch(isMosaicMode, (on) => {
  if (on) void mosaicStore.listPlans();
});
// The supervisor now re-tunes every mode's finish — deepsky/nebula LRGB composite, comet colour
// composite, milkyway grade, planetary sharpen — so every stacking mode in the picker supports it.
const supportsSupervise = computed(() => modes.includes(selectedMode.value));

// Advanced AI parameters: a free-text goal the agent carries for the run + fine tunable-knob
// overrides as a JSON object (same whitelist/clamps as the supervisor). The JSON is validated here —
// invalid text turns the field red and blocks the run; empty means "send nothing".
const goal = ref("");
// Imaging target for plate-solve/SPCC seeding (deepsky family): a catalogue name ("M66") or "RA,Dec"
// for captures whose FITS headers and folder names can't identify the field (e.g. SharpCap's
// "CapObj"). Optional — the backend discovers the target from the folder path when it can.
const target = ref("");
const paramsText = ref("");
const runParams = computed<Record<string, unknown> | null | undefined>(() => {
  const txt = paramsText.value.trim();
  if (!txt) return undefined; // nothing to send
  try {
    const v: unknown = JSON.parse(txt);
    if (v && typeof v === "object" && !Array.isArray(v))
      return v as Record<string, unknown>;
  } catch {
    // fall through: not valid JSON
  }
  return null; // invalid (unparsable, or not a JSON object)
});
const paramsInvalid = computed(() => runParams.value === null);

// Prefill: opening "Advanced AI parameters" (or switching mode while it's open) fills the JSON box with
// the mode's effective knobs so the user sees and tweaks the real values instead of an empty box. The
// knobs the checkboxes above already own are OMITTED — the backend applies the JSON *after* the
// checkboxes, so including them would silently override an unchecked box. `paramsDirty` protects manual
// edits: once the user types, we never re-prefill until the mode changes.
const CHECKBOX_PARAM_KEYS = [
  "color_calibration",
  "denoise_chroma",
  "denoise_lum",
  "ha_exclude_stars",
  "earthshine_gain", // owned by the planetary "Reveal earthshine" checkbox
  "mosaic", // owned by the deepsky-family "Mosaic canvas" checkbox
];
const paramsDirty = ref(false);
const advancedOpen = ref(false);
const applyingPreset = ref(false); // true while applyPreset() spreads a preset onto the form

// The selected mode's full effective knob map (pipeline.ParamsFor), cached in the store. Kept here so
// clicking a glossary row can insert that knob with its real default value — even while the box is dirty.
const modeDefaults = ref<Record<string, unknown>>({});
// The selected mode's per-knob min/max clamp bounds (pipeline.KnobRangesFor), shown in the glossary.
const modeRanges = ref<Record<string, KnobRange>>({});

// loadModeDefaults refreshes modeDefaults + modeRanges from the (cached) /api/mode-params fetch, keeping
// the last-known maps on failure so a transient error never wipes the toggle's source of defaults.
async function loadModeDefaults(): Promise<Record<string, unknown>> {
  try {
    const { defaults, ranges } = await jobsStore.fetchModeParams(
      selectedMode.value,
    );
    modeDefaults.value = defaults;
    modeRanges.value = ranges;
  } catch {
    // keep the last-known defaults
  }
  return modeDefaults.value;
}

async function prefillParams() {
  const defaults = await loadModeDefaults(); // also arms the click-to-add toggle below
  if (paramsDirty.value) return;
  if (Object.keys(defaults).length === 0) return; // fetch failed: leave the box as-is
  const shown: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(defaults)) {
    if (!CHECKBOX_PARAM_KEYS.includes(k)) shown[k] = v;
  }
  paramsText.value = JSON.stringify(shown, null, 2);
}

function onAdvancedToggle(e: Event) {
  advancedOpen.value = (e.target as HTMLDetailsElement).open;
  if (advancedOpen.value) void prefillParams();
}

// Switching mode invalidates any prior edits (each mode has its own knob set) → reset + re-prefill.
// A preset drives mode + knobs together (applyPreset), so skip the prefill while it is applying —
// otherwise the mode-change prefill would clobber the preset's recipe.
watch(selectedMode, () => {
  alignPointsResult.value = null;
  alignPointsError.value = "";
  resetSetQa(); // set IDs only apply to the deepsky-family scan they came from
  if (applyingPreset.value) return;
  paramsDirty.value = false;
  if (advancedOpen.value) void prefillParams();
});

// "Reset to defaults" re-prefills the box with the current mode's effective knobs.
function resetParams() {
  paramsDirty.value = false;
  void prefillParams();
}

// stringifySorted serialises a params object with alphabetically-ordered keys — matching the box's
// existing layout (Go json.Marshal sorts map keys, so prefill/preset JSON is already alphabetical).
function stringifySorted(obj: Record<string, unknown>): string {
  const sorted: Record<string, unknown> = {};
  for (const k of Object.keys(obj).sort()) sorted[k] = obj[k];
  return JSON.stringify(sorted, null, 2);
}

// toggleParam adds a knob to the JSON with its mode default (from modeDefaults), or removes it if already
// present — the click/keyboard action behind the advanced-params glossary. It no-ops on invalid JSON
// (nothing safe to merge into) and keeps the box alphabetical so add/remove stays stable.
// toggleParam cycles a knob through the glossary's 3 states: absent → its default → its "opposite"
// (bool flip / far end of the numeric range) → removed. Enums have no opposite and stay 2-state; a
// hand-edited value flips to the opposite first, then removes — the cycle always terminates.
async function toggleParam(key: string) {
  if (paramsInvalid.value) return;
  const defaults = Object.keys(modeDefaults.value).length
    ? modeDefaults.value
    : await loadModeDefaults();
  const next: Record<string, unknown> = { ...(runParams.value ?? {}) };
  if (!Object.prototype.hasOwnProperty.call(next, key)) {
    if (!Object.prototype.hasOwnProperty.call(defaults, key)) {
      return; // unknown key with no default — nothing to insert
    }
    next[key] = defaults[key];
  } else {
    const opp = oppositeOf(defaults[key], modeRanges.value[key]);
    if (opp !== undefined && next[key] !== opp) {
      next[key] = opp; // second click: flip to the opposite value
    } else {
      delete next[key]; // third click (or an enum's second): remove
    }
  }
  paramsText.value = Object.keys(next).length ? stringifySorted(next) : "";
  paramsDirty.value = true;
}

// nudgeParam steps an active numeric knob with the keyboard (↑/↓ on its glossary row), clamped min/max.
function nudgeParam(key: string, dir: 1 | -1) {
  if (paramsInvalid.value) return;
  const range = modeRanges.value[key];
  const next: Record<string, unknown> = { ...(runParams.value ?? {}) };
  if (!range || !Object.prototype.hasOwnProperty.call(next, key)) return;
  next[key] = nudged(next[key], dir, range, modeDefaults.value[key]);
  paramsText.value = stringifySorted(next);
  paramsDirty.value = true;
}

onMounted(async () => {
  s3.fetchStatus(); // learn whether S3 is configured (drives presence badges + transfer actions)
  await browseStore.browse();
  rootPath.value = browseStore.path;
  browseStore.loadProcessed(); // mark folders already used in a past processing
});

async function openDir(path: string) {
  await browseStore.browse(path);
}

// --- S3 storage --------------------------------------------------------------------------------------
// openS3Tab switches to the S3 tab and lists the real bucket at its root (only when configured).
function openS3Tab() {
  if (!s3.configured) return;
  sourceTab.value = "s3";
  if (s3.bucket) void s3.s3Browse("");
}
// Changing the bucket/prefix re-lists the S3 tab from the root (and refreshes the local presence badges);
// the previous S3 selection is cleared since it referred to the old bucket/prefix.
async function onBucket(e: Event) {
  s3.setBucket((e.target as HTMLSelectElement).value);
  s3.clearS3();
  // The new bucket invalidates both cached listings (presence badges + object tree).
  browseStore.clearCache();
  s3.clearS3Cache();
  await Promise.all([
    browseStore.browse(browseStore.path, true),
    s3.s3Browse("", true),
  ]);
}

// refreshS3 re-checks the connection and re-lists both trees live (bypassing every cache).
async function refreshS3() {
  browseStore.clearCache();
  s3.clearS3Cache();
  await s3.fetchStatus();
  await Promise.all([
    browseStore.browse(browseStore.path, true),
    s3.bucket ? s3.s3Browse(s3.s3Rel, true) : Promise.resolve(),
  ]);
}
async function onPrefix(e: Event) {
  s3.setPrefix((e.target as HTMLInputElement).value);
  s3.clearS3();
  browseStore.clearCache();
  s3.clearS3Cache();
  await Promise.all([
    browseStore.browse(browseStore.path, true),
    s3.s3Browse("", true),
  ]);
}

// relToRoot maps a selected folder's absolute path to its path relative to the capture root (DataDir),
// which is the transfer key. rootPath is the initial browse root (= DataDir).
function relToRoot(p: string): string {
  const root = rootPath.value;
  if (root && p.startsWith(root))
    return p.slice(root.length).replace(/^\/+/, "");
  return baseName(p);
}

// onTransfer enqueues one S3 transfer job per selected folder; each shows a progress bar in Tasks.
const transferToast = ref<{ n: number; op: TransferOp } | null>(null);
async function onTransfer(op: TransferOp) {
  const folders = browseStore.selected;
  if (!folders.length) return;
  let n = 0;
  for (const f of folders) {
    const rel = relToRoot(f.path);
    if (!rel) continue;
    try {
      await s3.transfer(op, rel);
      n++;
    } catch {
      // surfaced via the Tasks list if the job fails
    }
  }
  transferToast.value = { n, op };
}
// Cross-session reuse: discovered prior data + the user's selection.
const reusePreview = ref<ReusePreview | null>(null);
const reuseEnabled = ref(true);
const reuseSelected = ref<number[]>([]);
// Calibration suggestions from the library + the suggestion ids the user unchecked to skip.
const calibPreview = ref<CalibPreview | null>(null);
const calibExcluded = ref<string[]>([]);
// The joined per-session run plan (which masters pair with which night's lights) — the data behind
// the multi-night "Capture nights" breakdown.
const runPlan = ref<RunPlanPreview | null>(null);
// Force mismatched (gain/exposure/temperature) library masters onto the lights (relaxes the matcher).
const forceCalibration = ref(false);

// Pre-stack stray-light check (deepsky/nebula): the analysis report, the results modal, and the
// set IDs the user chose to exclude — threaded into the run as exclude_sets.
const setQaBusy = ref(false);
const setQaError = ref("");
const setQaReport = ref<SetQaReport | null>(null);
const setQaOpen = ref(false);
const excludedSets = ref<string[]>([]);

function resetSetQa() {
  setQaReport.value = null;
  setQaError.value = "";
  setQaOpen.value = false;
  excludedSets.value = [];
}

const reuseSessionIds = computed(() =>
  (reusePreview.value?.reuse.sessions ?? []).map((s) => s.session_id),
);
// Disabled if the user turned reuse off, or kept it on but deselected every session.
const reuseDisabledForRun = computed(
  () =>
    !reuseEnabled.value ||
    (reuseSessionIds.value.length > 0 && reuseSelected.value.length === 0),
);
// Send a session list only when it is a strict subset; empty = fold in all discovered sessions.
const reuseSelectionForRun = computed(() =>
  reuseSelected.value.length === reuseSessionIds.value.length
    ? []
    : reuseSelected.value,
);

// doInspect inspects a set of LOCAL capture folder paths (unions frames + reuse/calibration previews).
async function doInspect(paths: string[]) {
  selectedPaths.value = paths;
  await browseStore.inspect(paths);
  // Reuse + calibration previews are independent — fetch them together. The calibration preview honors
  // the (sticky) force toggle so a pre-checked "force" already shows the mismatched masters it will apply.
  const [reuse, calib, plan] = await Promise.all([
    jobsStore.previewReuse(paths),
    jobsStore.previewCalibration(paths, forceCalibration.value),
    jobsStore.previewPlan(paths, forceCalibration.value),
  ]);
  reusePreview.value = reuse;
  // Default: fold in every discovered prior session (user can deselect).
  reuseSelected.value = (reuse?.reuse.sessions ?? []).map((s) => s.session_id);
  reuseEnabled.value = true;
  // Default: include every matched library master (user can uncheck).
  calibPreview.value = calib;
  calibExcluded.value = [];
  runPlan.value = plan;
  // A new selection invalidates the stray-light report and its set IDs.
  resetSetQa();
}

// Toggling "force" re-matches the library: mismatched darks/flats/bias appear (or disappear) as
// suggestions, so the panel + frozen calib_plan reflect exactly what a forced run would apply.
watch(forceCalibration, async (on) => {
  if (!selectedPaths.value.length) return;
  calibPreview.value = await jobsStore.previewCalibration(
    selectedPaths.value,
    on,
  );
  calibExcluded.value = [];
});

// onInspect is the primary action for both tabs: download any S3-picked folders to local (kept local),
// then inspect the combined set (local selection + the downloaded S3 folders). Falls back to the emitted
// active local folder only when nothing is checked in either tab.
async function onInspect(emitted: string[]) {
  inspectError.value = "";
  const localSel = browseStore.selected.map((e) => e.path);
  const s3Rels = s3.s3Selected.map((e) => e.path);
  const localPaths =
    localSel.length || s3Rels.length
      ? localSel
      : emitted.filter((p) => p.startsWith(rootPath.value)); // ignore an S3-tab active rel
  if (s3Rels.length) {
    downloadingS3.value = s3Rels.length;
    downloadedS3.value = 0;
    try {
      await s3.importFolders(s3Rels, () => downloadedS3.value++);
      await browseStore.browse(browseStore.path); // downloaded folders now show in Local Files
    } catch (e) {
      inspectError.value = (e as Error).message;
      downloadingS3.value = 0;
      return;
    }
    downloadingS3.value = 0;
  }
  const landing = s3Rels.map((rel) => `${rootPath.value}/${rel}`);
  const paths = [...localPaths, ...landing];
  if (paths.length) {
    inspecting.value = true;
    try {
      await doInspect(paths);
    } finally {
      inspecting.value = false;
    }
  }
}

const inv = computed(() => browseStore.inventory);
const summary = useCaptureSummary(inv);
const { detectedFilters, mapping, overrides } = useChannelMapping(inv);
const isDeepskyFamily = computed(
  () => selectedMode.value === "deepsky" || selectedMode.value === "nebula",
);
// force_calibration_frames only affects the Siril-master modes (deepsky/nebula/planetary/comet); the
// milky-way (per-pixel phone calibration) and live-stacking paths calibrate differently and ignore it.
const calibForceApplies = computed(
  () =>
    isDeepskyFamily.value ||
    isPlanetary.value ||
    selectedMode.value === "comet",
);
// The filters a palette needs but the current input lacks (→ disable it in the selector, with a hint).
function paletteMissing(needs: string[]): string[] {
  return needs.filter((f) => !detectedFilters.value.includes(f));
}

// --- Processing presets -----------------------------------------------------------------------------
// The built-in "best params per situation" catalog + the user's saved presets. Applying one spreads its
// recipe onto the launch form; "Save current…" captures the form as a named preset (persisted in
// Postgres). The picker is keyed by a stable string so the currently-applied preset stays highlighted.
const presetsStore = usePresetsStore();
const selectedPresetKey = ref(""); // "" = custom (nothing applied)
const presetEdit = ref<"" | "save" | "rename" | "duplicate">(""); // which inline name input is showing
const presetNameField = ref("");
const presetError = ref("");
// The payload being duplicated, stashed at click time — the live form may not match the selected
// preset, and a built-in's payload only exists on the item, never in the form.
const duplicateSource = ref<PresetPayload | null>(null);

const presetKey = (p: PresetItem) => (p.builtin ? `b:${p.name}` : `u:${p.id}`);
const presetLabel = (p: PresetItem) =>
  p.builtin ? t(`preset.builtin.${p.name}.label`) : p.name;
const presetDesc = (p: PresetItem) =>
  p.builtin ? t(`preset.builtin.${p.name}.desc`) : "";
const selectedPreset = computed(
  () =>
    presetsStore.presets.find(
      (p) => presetKey(p) === selectedPresetKey.value,
    ) ?? null,
);
// Only user presets can be renamed/deleted (built-ins are read-only).
const selectedUserPreset = computed(() =>
  selectedPreset.value && !selectedPreset.value.builtin
    ? selectedPreset.value
    : null,
);

// applyPreset spreads a preset's payload onto the launch-form refs. It sets mode first, guarded by
// applyingPreset so the mode watcher does NOT re-prefill and wipe the recipe's knob JSON; only fields the
// payload defines are touched.
function applyPreset(item: PresetItem) {
  const p = item.payload;
  applyingPreset.value = true;
  if (p.mode) selectedMode.value = p.mode;
  if (p.format) selectedFormat.value = p.format;
  if (p.palette) palette.value = p.palette;
  if (p.look) look.value = p.look;
  if (p.brightness) brightness.value = p.brightness;
  if (p.color_calibration !== undefined)
    colorCalibration.value = p.color_calibration;
  if (p.denoise !== undefined) denoise.value = p.denoise;
  if (p.ha_exclude_stars !== undefined)
    haExcludeStars.value = p.ha_exclude_stars;
  if (typeof p.mosaic === "boolean") mosaic.value = p.mosaic;
  if (p.output_luminance !== undefined)
    outputLuminance.value = p.output_luminance;
  if (p.output_mono_stack !== undefined)
    outputMonoStack.value = p.output_mono_stack;
  if (p.drop_wheel_transition !== undefined)
    dropWheelTransition.value = p.drop_wheel_transition;
  if (p.supervise !== undefined) supervise.value = p.supervise;
  // Earthshine rides the params knob channel (earthshine_gain), so the checkbox mirrors the recipe.
  const presetGain = Number(
    (p.params as Record<string, unknown> | undefined)?.earthshine_gain,
  );
  earthshine.value = Number.isFinite(presetGain) && presetGain > 0;
  goal.value = p.goal ?? "";

  const hasParams = !!p.params && Object.keys(p.params).length > 0;
  if (hasParams) {
    paramsText.value = JSON.stringify(p.params, null, 2);
    paramsDirty.value = true; // protect the recipe from the mode-watch prefill
    advancedOpen.value = true; // reveal the knobs
  } else {
    paramsText.value = "";
    paramsDirty.value = false; // no recipe → let the box reflect the new mode when opened
    if (advancedOpen.value) void prefillParams();
  }
  // Release the guard AFTER the mode watcher has flushed (it runs pre-render; nextTick is after).
  void nextTick(() => {
    applyingPreset.value = false;
  });
}

// capturePayload snapshots the situation-defining form fields (the same subset runOpts sends, minus
// input-specific ones) into a preset payload.
function capturePayload(): PresetPayload {
  const payload: PresetPayload = {
    mode: selectedMode.value,
    format: selectedFormat.value,
    color_calibration: colorCalibration.value,
    denoise: denoise.value,
    ha_exclude_stars: haExcludeStars.value,
    drop_wheel_transition: dropWheelTransition.value,
    supervise: supervise.value,
  };
  if (isDeepskyFamily.value) {
    payload.palette = palette.value;
    payload.output_luminance = outputLuminance.value;
    payload.output_mono_stack = outputMonoStack.value;
  }
  if (isMilkyway.value) {
    payload.look = look.value;
    payload.brightness = brightness.value;
  }
  const g = goal.value.trim();
  if (g) payload.goal = g;
  const params = effectiveRunParams();
  if (params) {
    payload.params = params;
  }
  return payload;
}

// effectiveRunParams merges the planetary "Reveal earthshine" checkbox into the Advanced-JSON knobs:
// the option is the params-only earthshine_gain — an explicit positive value typed in the JSON wins,
// otherwise the checkbox sends the natural 1.0. Unchecked leaves the JSON exactly as typed (an
// advanced user's explicit gain still applies).
function effectiveRunParams(): Record<string, unknown> | undefined {
  const out: Record<string, unknown> = { ...(runParams.value ?? {}) };
  if (isPlanetary.value && earthshine.value) {
    const typed = Number(out.earthshine_gain);
    out.earthshine_gain = Number.isFinite(typed) && typed > 0 ? typed : 1;
  }
  return Object.keys(out).length ? out : undefined;
}

// runAlignPointsEstimate fits the first luminance frame of the selection server-side, shows the
// usable-point count, and fills align_points in the fine-parameters JSON (paramsDirty guards the
// merge from the mode-watch prefill; advancedOpen reveals the filled box — mirrors applyPreset).
async function runAlignPointsEstimate() {
  if (
    !selectedPaths.value.length ||
    paramsInvalid.value ||
    alignPointsBusy.value
  )
    return;
  alignPointsBusy.value = true;
  alignPointsError.value = "";
  try {
    const est = await jobsStore.estimateAlignPoints(
      selectedPaths.value,
      alignPointsMinPx.value ?? 0,
    );
    alignPointsResult.value = est;
    const next: Record<string, unknown> = { ...(runParams.value ?? {}) };
    next.align_points = est.suggested_align_points;
    paramsText.value = stringifySorted(next);
    paramsDirty.value = true;
    advancedOpen.value = true;
  } catch (e) {
    alignPointsResult.value = null;
    alignPointsError.value = (e as Error).message;
  } finally {
    alignPointsBusy.value = false;
  }
}

// runSetQaAnalysis probes the selection's light sets for stray-light artifacts server-side and
// opens the results modal — even with zero flagged sets, so a clean report is verifiable. The
// modal's choice lands in excludedSets and applies at launch via exclude_sets.
async function runSetQaAnalysis() {
  if (!selectedPaths.value.length || setQaBusy.value) return;
  setQaBusy.value = true;
  setQaError.value = "";
  try {
    setQaReport.value = await jobsStore.analyzeSetQuality(selectedPaths.value);
    setQaOpen.value = true;
  } catch (e) {
    setQaReport.value = null;
    setQaError.value = (e as Error).message;
  } finally {
    setQaBusy.value = false;
  }
}

function applySetExclusions(ids: string[]) {
  excludedSets.value = ids;
  setQaOpen.value = false;
}

// excludedSetLabel names an excluded set for its chip: filter · night · exposure.
function excludedSetLabel(id: string): string {
  const s = setQaReport.value?.sets.find((x) => x.id === id);
  if (!s) return id;
  const parts = [s.key.filter || "?"];
  if (s.key.session) parts.push(s.key.session);
  parts.push(humanizeMs(s.key.exposure_ms));
  return parts.join(" · ");
}

function onPresetChange() {
  presetError.value = "";
  const p = selectedPreset.value;
  if (p) applyPreset(p);
}

// defaultPresetName pre-fills the save field with a self-describing summary of the captured
// recipe — <mode>_<palette/look/ai>_<first knobs+values>(+N) — so a saved preset is recognizable
// without typing (e.g. "planetary_best_percent30_clahe1.5_headroom0.85+2"). Deduped against the
// existing presets because save() upserts by name (a colliding default would silently overwrite).
function defaultPresetName(): string {
  const parts: string[] = [selectedMode.value];
  if (isDeepskyFamily.value && palette.value && palette.value !== "natural")
    parts.push(palette.value);
  if (isMilkyway.value && look.value) parts.push(look.value);
  if (supervise.value) parts.push("ai");
  const knobs = effectiveRunParams() ?? {};
  const keys = Object.keys(knobs).sort();
  for (const k of keys.slice(0, 3)) {
    const v = knobs[k];
    parts.push(
      typeof v === "boolean" ? (v ? k : `no-${k}`) : `${k}${String(v)}`,
    );
  }
  let name = parts.join("_");
  if (keys.length > 3) name += `+${keys.length - 3}`;
  const taken = new Set(
    presetsStore.userPresets.map((p) => p.name.toLowerCase()),
  );
  if (!taken.has(name.toLowerCase())) return name;
  for (let i = 2; ; i++) {
    if (!taken.has(`${name}_${i}`.toLowerCase())) return `${name}_${i}`;
  }
}

function startPresetSave() {
  presetError.value = "";
  presetNameField.value = defaultPresetName();
  presetEdit.value = "save";
}
function startPresetRename() {
  const p = selectedUserPreset.value;
  if (!p) return;
  presetError.value = "";
  presetNameField.value = p.name;
  presetEdit.value = "rename";
}
// Duplicate works on built-ins too — the copy becomes a normal, editable user preset (the only way
// to fork a read-only built-in recipe).
function startPresetDuplicate() {
  const p = selectedPreset.value;
  if (!p) return;
  presetError.value = "";
  duplicateSource.value = p.payload;
  presetNameField.value = duplicateName(presetLabel(p));
  presetEdit.value = "duplicate";
}
// duplicateName builds "<label> copy", deduped ("… 2", "… 3") against the user presets — save()
// upserts by case-insensitive name, so a colliding default would silently overwrite.
function duplicateName(label: string): string {
  const base = `${label} ${t("preset.copySuffix")}`;
  const taken = new Set(
    presetsStore.userPresets.map((p) => p.name.toLowerCase()),
  );
  if (!taken.has(base.toLowerCase())) return base;
  for (let i = 2; ; i++) {
    if (!taken.has(`${base} ${i}`.toLowerCase())) return `${base} ${i}`;
  }
}
function cancelPresetEdit() {
  presetEdit.value = "";
  presetNameField.value = "";
  presetError.value = "";
  duplicateSource.value = null;
}
async function confirmPresetEdit() {
  const name = presetNameField.value.trim();
  if (!name) return;
  try {
    if (presetEdit.value === "rename") {
      const p = selectedUserPreset.value;
      if (!p) return;
      await presetsStore.rename(p.id, name);
    } else if (presetEdit.value === "duplicate") {
      const payload = duplicateSource.value;
      if (!payload) return;
      // A duplicate must never silently overwrite (save() upserts by name) — reject collisions.
      const clash = presetsStore.userPresets.some(
        (p) => p.name.toLowerCase() === name.toLowerCase(),
      );
      if (clash) {
        presetError.value = t("preset.nameTaken");
        return;
      }
      await presetsStore.save(name, payload);
      const saved = presetsStore.userPresets.find(
        (p) => p.name.toLowerCase() === name.toLowerCase(),
      );
      if (saved) selectedPresetKey.value = presetKey(saved);
    } else {
      if (paramsInvalid.value) return;
      await presetsStore.save(name, capturePayload());
      const saved = presetsStore.userPresets.find(
        (p) => p.name.toLowerCase() === name.toLowerCase(),
      );
      if (saved) selectedPresetKey.value = presetKey(saved);
    }
    cancelPresetEdit();
  } catch (e) {
    presetError.value = (e as Error).message;
  }
}
async function deleteSelectedPreset() {
  const p = selectedUserPreset.value;
  if (!p) return;
  await presetsStore.remove(p.id);
  if (selectedPresetKey.value === presetKey(p)) selectedPresetKey.value = "";
}
// Star/unstar the selected user preset — favorites sort first in the picker. Built-ins have no DB row
// to star (duplicate one first to get a starrable copy).
async function toggleFavoriteSelected() {
  const p = selectedUserPreset.value;
  if (!p) return;
  await presetsStore.setFavorite(p.id, !p.favorite);
}

onMounted(() => {
  void presetsStore.list();
});

const counts = computed(() => {
  const c: Record<string, number> = {};
  for (const f of inv.value?.frames ?? []) c[f.type] = (c[f.type] || 0) + 1;
  return c;
});

// Calibration-only selection (darks/flats/bias, zero lights): the launch button becomes "build the
// masters & add them to the library" — a masters-only job instead of a pipeline run.
const mastersOnly = computed(() => {
  const c = counts.value;
  const cal = (c.DARK || 0) + (c.FLAT || 0) + (c.BIAS || 0) + (c.DARKFLAT || 0);
  return !!inv.value && !(c.LIGHT || 0) && cal > 0;
});

type Row = Record<string, unknown>;
function rowsFor(types: string[]): Row[] {
  return (inv.value?.sets ?? [])
    .filter((s: FrameSet) => types.includes(s.key.type))
    .map((s: FrameSet) => ({
      type: s.key.type,
      object: s.key.object || "",
      filter: s.key.filter || "",
      exposure_ms: s.key.exposure_ms,
      count: s.count,
      integration: s.total_integration_ms,
      gain: s.key.gain,
      offset: s.key.offset,
      iso: s.key.iso || 0,
      temp: s.key.temp_bucket_c,
    }));
}
const lightRows = computed(() => rowsFor(["LIGHT"]));
const calibRows = computed(() => rowsFor(["DARK", "FLAT", "DARKFLAT", "BIAS"]));

const ms = (v: unknown) => humanizeMs(Number(v));
const degC = (v: unknown) => `${v}°C`;
// ISO shows only for phone/DSLR raws; blank for cooled-camera sets (ISO 0).
const isoFmt = (v: unknown) => (Number(v) > 0 ? String(Number(v)) : "");

const lightColumns: Column<Row>[] = [
  {
    key: "object",
    label: t("fields.object"),
    sortable: true,
    searchable: true,
  },
  {
    key: "filter",
    label: t("fields.filter"),
    sortable: true,
    searchable: true,
  },
  {
    key: "exposure_ms",
    label: t("fields.exposure"),
    sortable: true,
    format: ms,
  },
  { key: "count", label: t("fields.count"), sortable: true, align: "right" },
  {
    key: "integration",
    label: t("fields.integration"),
    sortable: true,
    format: ms,
    align: "right",
  },
  { key: "gain", label: t("fields.gain"), sortable: true, align: "right" },
  { key: "offset", label: t("fields.offset"), sortable: true, align: "right" },
  {
    key: "iso",
    label: t("fields.iso"),
    sortable: true,
    format: isoFmt,
    align: "right",
  },
  {
    key: "temp",
    label: t("fields.temp"),
    sortable: true,
    format: degC,
    align: "right",
  },
];
const calibColumns: Column<Row>[] = [
  { key: "type", label: t("fields.type"), sortable: true, searchable: true },
  {
    key: "filter",
    label: t("fields.filter"),
    sortable: true,
    searchable: true,
  },
  {
    key: "exposure_ms",
    label: t("fields.exposure"),
    sortable: true,
    format: ms,
  },
  { key: "count", label: t("fields.count"), sortable: true, align: "right" },
  { key: "gain", label: t("fields.gain"), sortable: true, align: "right" },
  { key: "offset", label: t("fields.offset"), sortable: true, align: "right" },
  {
    key: "iso",
    label: t("fields.iso"),
    sortable: true,
    format: isoFmt,
    align: "right",
  },
  {
    key: "temp",
    label: t("fields.temp"),
    sortable: true,
    format: degC,
    align: "right",
  },
];

// Individual files (with paths) for the click-to-view file viewer — the set tables omit frame paths.
const fileRows = computed<Row[]>(() =>
  (inv.value?.frames ?? []).map((f) => ({
    name: f.path.split("/").pop() || f.path,
    path: f.path,
    type: f.type,
    filter: f.filter || "",
    exposure_ms: f.exposure_ms,
    iso: f.iso || 0,
    dims: f.width && f.height ? `${f.width}×${f.height}` : "",
  })),
);
const fileColumns: Column<Row>[] = [
  { key: "name", label: t("fields.file"), sortable: true, searchable: true },
  { key: "type", label: t("fields.type"), sortable: true, searchable: true },
  {
    key: "filter",
    label: t("fields.filter"),
    sortable: true,
    searchable: true,
  },
  {
    key: "exposure_ms",
    label: t("fields.exposure"),
    sortable: true,
    format: ms,
  },
  {
    key: "iso",
    label: t("fields.iso"),
    sortable: true,
    format: isoFmt,
    align: "right",
  },
  { key: "dims", label: t("fields.dimensions") },
  { key: "view", label: "", align: "right" },
];

const canRun = computed(
  () =>
    selectedPaths.value.length > 0 &&
    !!inv.value &&
    !launching.value &&
    !paramsInvalid.value,
);

// One path → show it; several → a count (the pills in the browser already list them).
const summaryPath = computed(() => {
  if (selectedPaths.value.length === 1) return selectedPaths.value[0];
  if (selectedPaths.value.length)
    return t("import.nFolders", { n: selectedPaths.value.length });
  return "";
});

// The run options shared by "Run" (immediate) and "Add to queue" (sequential lane).
function runOpts(): CreateOpts {
  return {
    // Calibration-only selection → a masters-only build job (no lights, no image).
    buildMasters: mastersOnly.value || undefined,
    paths: selectedPaths.value,
    filterMap: overrides.value,
    colorCalibration: colorCalibration.value,
    denoise: denoise.value,
    haExcludeStars: haExcludeStars.value,
    mosaic: isDeepskyFamily.value && mosaic.value ? true : undefined,
    // Extra mono side-outputs are deepsky/nebula-only; omit for other modes (they ignore them).
    outputLuminance: isDeepskyFamily.value ? outputLuminance.value : undefined,
    outputMonoStack: isDeepskyFamily.value ? outputMonoStack.value : undefined,
    dropWheelTransition: dropWheelTransition.value,
    supervise: supportsSupervise.value && supervise.value,
    target: isDeepskyFamily.value
      ? target.value.trim() || undefined
      : undefined,
    // Advanced AI parameters (empty goal / empty-or-absent params are simply not sent).
    goal: goal.value.trim() || undefined,
    params: effectiveRunParams(),
    look: isMilkyway.value ? look.value : undefined,
    palette:
      isDeepskyFamily.value && palette.value !== "natural"
        ? palette.value
        : undefined,
    brightness: isMilkyway.value ? brightness.value : undefined,
    orientation: isMilkyway.value ? orientationValue.value : undefined,
    darkDir: isMilkyway.value ? darkDir.value || undefined : undefined,
    flatDir: isMilkyway.value ? flatDir.value || undefined : undefined,
    biasDir: isMilkyway.value ? biasDir.value || undefined : undefined,
    inventory: inv.value,
    reuseDisabled: reuseDisabledForRun.value,
    // Only send a session list when the user deselected some; empty = fold in all discovered.
    reuseSessions: reuseSelectionForRun.value,
    // Library masters the user unchecked in the Calibration panel (skipped at process time).
    calibExclude: calibExcluded.value,
    // Light sets excluded in the stray-light check (deepsky/nebula; dropped before grouping).
    excludeSets: isDeepskyFamily.value ? excludedSets.value : undefined,
    // Force mismatched (gain/exposure/temperature) masters onto the lights (relaxes the matcher).
    forceCalibration: forceCalibration.value,
    // Freeze the matched-calibration preview with the job so its page can show the included darks/flats/
    // bias and their params (the pipeline still re-matches independently, honoring calibExclude).
    calibPlan: calibPreview.value,
    // Tiled-mosaic run: reference the saved plan (panel labels, expected centers, solve hints).
    mosaicPlanId:
      isMosaicMode.value && mosaicPlanId.value ? mosaicPlanId.value : undefined,
    // Full-S3 run: pull inputs from S3, process, push inputs+results, then free local (only when active).
    storageMode: s3.active && processMode.value === "s3" ? "s3" : undefined,
    s3:
      s3.active && processMode.value === "s3"
        ? { bucket: s3.bucket, prefix: s3.prefix }
        : undefined,
    lowDisk:
      s3.active && processMode.value === "s3" && isDeepskyFamily.value
        ? lowDisk.value
        : undefined,
  };
}

async function runPipeline() {
  launching.value = true;
  try {
    const id = await jobsStore.create(
      selectedPaths.value[0],
      selectedMode.value,
      selectedFormat.value,
      runOpts(),
    );
    router.push({ name: "job", params: { id: String(id) } });
  } finally {
    launching.value = false;
  }
}

// Add to queue: enqueue a sequential job and stay on the page so the user can stack more. The chain runs
// one-at-a-time, auto-advancing — visible in the Tasks tab.
const queuedCount = ref(0);
async function queuePipeline() {
  launching.value = true;
  try {
    await jobsStore.create(
      selectedPaths.value[0],
      selectedMode.value,
      selectedFormat.value,
      { ...runOpts(), sequential: true },
    );
    queuedCount.value++;
    browseStore.loadProcessed(); // surface the just-queued set in the Processing history
  } finally {
    launching.value = false;
  }
}

// Re-run a past folder-set: re-select the folders that still exist, restore mode/format, inspect, and
// scroll to the run controls. Deleted folders are dropped (the chips show them crossed-out).
const runControls = ref<HTMLElement | null>(null);
// useHistory re-runs a past folder-set whether its files are still local or were freed to S3 after a
// full-S3 run. Folders present on the S3 mirror but not on disk (exists && !local) are pulled back from
// <prefix>/data/<rel> into <DataDir>/<rel> first, so the inspection below always sees real local files.
async function useHistory(entry: ProcessingHistoryEntry) {
  inspectError.value = "";
  // Partition the folder-set. `local === false` (strict) is the only signal to pull from S3, so an older
  // backend that omits `local` safely degrades to the local-only path instead of pulling every folder.
  const localPaths = entry.paths
    .filter((p) => p.exists && p.local !== false)
    .map((p) => p.path);
  const s3Only = entry.paths.filter((p) => p.exists && p.local === false);
  if (!localPaths.length && !s3Only.length) return; // every folder truly gone

  const landing: string[] = [];
  if (s3Only.length) {
    if (!s3.active) {
      inspectError.value = t("import.history.needS3");
      return;
    }
    // Pull with the backend-authoritative DataDir-rel (the ledger key the offer was based on) — a
    // client-side rel guess diverges for nested folders and misses the ledger. A data-namespace download
    // of `rel` lands at <DataDir>/<rel>, which is exactly `p.path`, so inspect the original paths below.
    const pulls = s3Only.filter((p) => p.rel);
    const rels = pulls.map((p) => p.rel);
    downloadingS3.value = rels.length;
    downloadedS3.value = 0;
    try {
      await s3.downloadFolders(rels, () => downloadedS3.value++);
    } catch (e) {
      inspectError.value = (e as Error).message;
      downloadingS3.value = 0;
      return;
    }
    downloadingS3.value = 0;
    await browseStore.browse(browseStore.path); // pulled folders now show in Local Files
    landing.push(...pulls.map((p) => p.path));
  }

  const paths = [...localPaths, ...landing];
  if (!paths.length) return;
  browseStore.selectPaths(paths);
  if (entry.mode && modes.includes(entry.mode)) selectedMode.value = entry.mode;
  if (entry.format && formats.includes(entry.format))
    selectedFormat.value = entry.format;
  inspecting.value = true;
  try {
    await doInspect(paths);
  } finally {
    inspecting.value = false;
  }
  await nextTick();
  runControls.value?.scrollIntoView({ behavior: "smooth", block: "center" });
}

// Naming/starring a history entry (saved selections): one inline edit at a time, keyed by the
// row's signature. star=true routes an unnamed row's ☆ through the naming input, so the star is
// saved with the name in one call (favorites exist only on named selections).
const histEdit = ref<{
  signature: string;
  draft: string;
  star: boolean;
} | null>(null);
const histError = ref("");

function startHistName(entry: ProcessingHistoryEntry, star = false) {
  histError.value = "";
  histEdit.value = {
    signature: entry.signature,
    draft: entry.selection?.name || entry.object || "",
    star,
  };
}
function cancelHistName() {
  histEdit.value = null;
  histError.value = "";
}
async function confirmHistName(entry: ProcessingHistoryEntry) {
  const edit = histEdit.value;
  const name = edit?.draft.trim();
  if (!edit || !name) return;
  try {
    if (entry.selection) {
      await browseStore.renameSelection(entry.selection.id, name);
    } else {
      await browseStore.saveSelection(name, entry, edit.star || undefined);
    }
    cancelHistName();
  } catch {
    histError.value = t("import.history.nameTaken"); // the only expected failure: 409 collision
  }
}
async function toggleHistFavorite(entry: ProcessingHistoryEntry) {
  histError.value = "";
  if (!entry.selection) {
    startHistName(entry, true);
    return;
  }
  try {
    await browseStore.setSelectionFavorite(
      entry.selection.id,
      !entry.selection.favorite,
    );
  } catch (e) {
    histError.value = (e as Error).message;
  }
}
async function forgetHistSelection(entry: ProcessingHistoryEntry) {
  if (!entry.selection) return;
  histError.value = "";
  try {
    await browseStore.deleteSelection(entry.selection.id);
  } catch (e) {
    histError.value = (e as Error).message;
  }
}

// Chip style for a history folder: muted + struck-through when the folder no longer exists on disk.
function histChip(exists: boolean): string {
  return exists
    ? "inline-flex items-center gap-1 rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300"
    : "inline-flex items-center gap-1 rounded-md border border-dashed border-slate-300 px-2 py-1 text-xs text-slate-400 line-through dark:border-slate-600 dark:text-slate-500";
}
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-semibold">{{ t("import.title") }}</h1>
      <p class="text-sm text-slate-500 dark:text-slate-400">
        {{ t("import.hint") }}
      </p>
    </div>

    <div :class="card">
      <!-- Source tabs: browse local disk vs the S3 mirror; the selection is shared across both. -->
      <div class="mb-4 flex gap-2">
        <button :class="tabClass('local')" @click="sourceTab = 'local'">
          {{ t("import.tabs.local") }}
        </button>
        <button
          :class="tabClass('s3')"
          :disabled="!s3.configured"
          :title="!s3.configured ? t('import.s3NotConfigured') : ''"
          @click="openS3Tab"
        >
          {{ t("import.tabs.s3") }}
        </button>
      </div>

      <!-- S3 storage config (S3 tab only): pick a bucket/prefix to see the mirror + enable transfers. -->
      <div
        v-if="sourceTab === 's3' && s3.configured"
        class="mb-3 flex flex-wrap items-center gap-2 text-xs"
      >
        <span class="font-medium text-slate-500 dark:text-slate-400">{{
          t("s3.title")
        }}</span>
        <select
          v-if="s3.buckets.length"
          :value="s3.bucket"
          :class="input"
          class="!w-auto !py-1"
          @change="onBucket"
        >
          <option value="">{{ t("s3.pickBucket") }}</option>
          <option v-for="b in s3.buckets" :key="b" :value="b">{{ b }}</option>
        </select>
        <input
          v-else
          :value="s3.bucket"
          :class="input"
          class="!w-40 !py-1"
          :placeholder="t('s3.bucket')"
          @change="onBucket"
        />
        <input
          :value="s3.prefix"
          :class="input"
          class="!w-40 !py-1"
          :placeholder="t('s3.prefix')"
          @change="onPrefix"
        />
        <button :class="btnGhost" class="!px-2 !py-1" @click="refreshS3">
          {{ t("s3.test") }}
        </button>
        <span
          v-if="s3.reachable"
          class="inline-flex items-center gap-1 text-success-600 dark:text-success-300"
          >● {{ t("s3.connected") }}</span
        >
        <span v-else-if="s3.status?.error" class="text-danger">{{
          s3.status.error
        }}</span>
      </div>
      <p
        v-else-if="sourceTab === 's3'"
        class="mb-3 text-xs text-slate-400 dark:text-slate-500"
      >
        {{ t("import.s3NotConfigured") }}
      </p>

      <!-- Local tab: the local filesystem (with S3-mirror presence badges for the sync/backup workflow). -->
      <FileBrowser
        v-if="sourceTab === 'local'"
        :path="browseStore.path"
        :root="rootPath"
        :entries="browseStore.entries"
        :loading="browseStore.loading"
        :selected="browseStore.selected"
        :error="browseStore.error"
        :fetch-children="browseStore.listDir"
        :processed="browseStore.processedByPath"
        :s3-enabled="s3.active"
        source-filter="local"
        :downloading="s3Busy"
        :busy-label="s3BusyLabel"
        @navigate="openDir"
        @inspect="onInspect"
        @toggle="browseStore.toggleSelected"
        @clear-selection="browseStore.clearSelected"
        @transfer="onTransfer"
      />
      <!-- S3 tab: the real bucket at <prefix>/<rel> (default connection). Picked folders download to
           <DataDir>/<rel> on inspect and become normal local captures. -->
      <FileBrowser
        v-else
        :path="s3.s3Rel"
        root=""
        :entries="s3.s3Entries"
        :loading="s3.loading"
        :selected="s3.s3Selected"
        :error="s3.error"
        :fetch-children="s3.s3ListDir"
        :downloading="s3Busy"
        :busy-label="s3BusyLabel"
        @navigate="s3.s3Browse"
        @inspect="onInspect"
        @toggle="s3.toggleS3"
        @clear-selection="s3.clearS3"
      />
      <div
        v-if="s3Busy"
        class="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-slate-500 dark:text-slate-400"
      >
        <Spinner>
          <span v-if="downloadingS3 > 0">{{
            t("import.downloadingS3Progress", {
              done: downloadedS3,
              n: downloadingS3,
            })
          }}</span>
          <span v-else>{{ t("import.inspectingS3") }}</span>
        </Spinner>
        <router-link
          v-if="downloadingS3 > 0"
          :to="{ name: 'jobs' }"
          class="font-medium underline hover:text-slate-700 dark:hover:text-slate-200"
          >{{ t("import.viewQueue") }}</router-link
        >
      </div>
      <p v-if="inspectError" class="mt-2 text-xs text-danger">
        {{ inspectError }}
      </p>
      <p
        v-if="transferToast"
        class="mt-2 text-xs text-success-600 dark:text-success-300"
      >
        {{ t("s3.queued", { n: transferToast.n }) }}
        <router-link
          :to="{ name: 'jobs' }"
          class="font-medium underline hover:text-success-700 dark:hover:text-success-200"
        >
          {{ t("import.viewQueue") }}
        </router-link>
      </p>
    </div>

    <!-- Processing history: re-run a past folder-set (deleted folders shown crossed-out) -->
    <CollapsibleCard
      v-if="browseStore.processingHistory.length"
      :title="t('import.history.title')"
      storage-key="astrostack.import.history"
    >
      <ul class="max-h-72 space-y-3 overflow-y-auto">
        <li
          v-for="entry in browseStore.processingHistory"
          :key="entry.signature"
          class="rounded-md border border-slate-200 p-2 dark:border-slate-700"
        >
          <div class="flex flex-wrap items-center gap-2">
            <StatusPill v-if="entry.jobId" :status="entry.status" />
            <span
              v-else
              class="rounded bg-brand-500/10 px-1.5 py-0.5 text-[10px] font-medium text-brand-500"
              >{{ t("import.history.saved") }}</span
            >
            <button
              class="text-sm"
              :class="
                entry.selection?.favorite
                  ? 'text-amber-400'
                  : 'text-slate-400 hover:text-amber-400'
              "
              :title="
                t(
                  entry.selection?.favorite
                    ? 'import.history.unfavorite'
                    : 'import.history.favorite',
                )
              "
              @click="toggleHistFavorite(entry)"
            >
              {{ entry.selection?.favorite ? "★" : "☆" }}
            </button>
            <template v-if="histEdit?.signature === entry.signature">
              <input
                v-model="histEdit.draft"
                :class="input"
                class="!w-44 !py-1 text-sm"
                :placeholder="t('import.history.namePlaceholder')"
                @keyup.enter="confirmHistName(entry)"
                @keyup.esc="cancelHistName"
              />
              <button
                :class="btnGhost"
                class="!px-2 !py-1 !text-xs"
                :disabled="!histEdit.draft.trim()"
                @click="confirmHistName(entry)"
              >
                {{ t("preset.saveBtn") }}
              </button>
              <button
                :class="btnGhost"
                class="!px-2 !py-1 !text-xs"
                @click="cancelHistName"
              >
                {{ t("preset.cancel") }}
              </button>
            </template>
            <template v-else>
              <span class="text-sm font-medium">{{
                entry.selection?.name ||
                entry.object ||
                t("import.history.untitled")
              }}</span>
              <button
                class="text-xs text-slate-400 hover:text-brand-500"
                :title="
                  t(
                    entry.selection
                      ? 'import.history.rename'
                      : 'import.history.name',
                  )
                "
                @click="startHistName(entry)"
              >
                ✎
              </button>
              <button
                v-if="entry.selection"
                class="text-xs text-slate-400 hover:text-danger"
                :title="t('import.history.forget')"
                @click="forgetHistSelection(entry)"
              >
                ✕
              </button>
            </template>
            <span class="text-xs text-slate-400">
              <template
                v-if="
                  entry.selection &&
                  entry.object &&
                  entry.selection.name !== entry.object
                "
                >{{ entry.object }} ·
              </template>
              {{ entry.mode ? t("run.modes." + entry.mode) + " · " : "" }}
              <template v-if="entry.jobId">{{
                formatTimestamp(entry.createdAtMs)
              }}</template>
              <template v-else>{{ t("import.history.saved") }}</template>
              <template v-if="entry.runs > 1">
                · {{ t("import.history.runs", { n: entry.runs }) }}</template
              >
            </span>
            <button
              :class="btnGhost"
              class="ml-auto !px-2 !py-1 !text-xs"
              :disabled="s3Busy || !entry.paths.some((p) => p.exists)"
              @click="useHistory(entry)"
            >
              {{ t("import.history.useAgain") }}
            </button>
          </div>
          <div class="mt-1.5 flex flex-wrap gap-1.5">
            <span
              v-for="p in entry.paths"
              :key="p.path"
              :class="histChip(p.exists)"
              :title="
                !p.exists
                  ? t('import.history.deleted') + ': ' + p.path
                  : p.local
                    ? p.path
                    : t('import.history.onS3') + ': ' + p.path
              "
            >
              <IconCloud
                v-if="p.exists && !p.local"
                class="h-3 w-3 shrink-0 text-brand-500 dark:text-brand-300"
              />
              <IconFolder v-else class="h-3 w-3 shrink-0" />
              {{ baseName(p.path) }}
            </span>
          </div>
        </li>
      </ul>
      <p v-if="histError" class="mt-2 text-xs text-danger">{{ histError }}</p>
      <!-- Feedback while a history re-run pulls its freed folders back from the S3 mirror. -->
      <div
        v-if="s3Busy"
        class="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-slate-500 dark:text-slate-400"
      >
        <Spinner>
          <span v-if="downloadingS3 > 0">{{
            t("import.downloadingS3Progress", {
              done: downloadedS3,
              n: downloadingS3,
            })
          }}</span>
          <span v-else>{{ t("import.inspectingS3") }}</span>
        </Spinner>
        <router-link
          v-if="downloadingS3 > 0"
          :to="{ name: 'jobs' }"
          class="font-medium underline hover:text-slate-700 dark:hover:text-slate-200"
          >{{ t("import.viewQueue") }}</router-link
        >
      </div>
      <p v-if="inspectError" class="mt-3 text-xs text-danger">
        {{ inspectError }}
      </p>
    </CollapsibleCard>

    <!-- Selected capture + channel mapping + run controls -->
    <div v-if="inv" class="grid gap-4 lg:grid-cols-2">
      <CaptureSummary :summary="summary" :path="summaryPath" />
      <FilterMappingEditor
        v-if="detectedFilters.length"
        v-model="mapping"
        :detection="inv.channel_detection"
        :detected-filters="detectedFilters"
      />
    </div>

    <!-- Multi-night selection: per-night breakdown + calibration mapping (renders nothing single-night). -->
    <SessionBreakdown
      v-if="inv?.sessions && inv.sessions.length > 1"
      :sessions="inv.sessions"
      :plan="runPlan"
    />

    <!-- Reuse + calibration advisories sit side-by-side on wide screens (both fold prior data into the run). -->
    <TwoPane v-if="inv" split="even">
      <template #main>
        <ReusePanel
          v-model:enabled="reuseEnabled"
          v-model:selected="reuseSelected"
          :preview="reusePreview"
        />
      </template>
      <template #aside>
        <CalibrationPanel
          v-model:excluded="calibExcluded"
          :preview="calibPreview"
        />
      </template>
    </TwoPane>

    <div ref="runControls" :class="card">
      <!-- Environment warnings (missing/broken tools, catalogues) — warn before the run, not after. -->
      <EnvWarnings class="mb-3" />

      <!-- Processing presets: apply a built-in "best params per situation" recipe (or a saved one), and
           save the current params as a named preset. -->
      <div class="mb-3 flex flex-wrap items-center gap-2">
        <span class="text-xs font-medium text-slate-500">{{
          t("preset.label")
        }}</span>
        <select
          v-model="selectedPresetKey"
          :class="[input, 'max-w-[16rem]']"
          data-demo="run-preset"
          @change="onPresetChange"
        >
          <option value="">{{ t("preset.custom") }}</option>
          <optgroup
            v-for="g in presetsStore.byCategory"
            :key="g.key"
            :label="
              g.key === 'mine' ? t('preset.my') : t('preset.category.' + g.key)
            "
          >
            <option
              v-for="p in g.items"
              :key="presetKey(p)"
              :value="presetKey(p)"
              :title="presetDesc(p)"
            >
              {{ (p.favorite ? "★ " : "") + presetLabel(p) }}
            </option>
          </optgroup>
        </select>

        <template v-if="presetEdit === ''">
          <button type="button" :class="btnGhost" @click="startPresetSave">
            {{ t("preset.save") }}
          </button>
          <button
            v-if="selectedPreset"
            type="button"
            :class="[btnGhost, '!px-2']"
            :title="t('preset.duplicate')"
            @click="startPresetDuplicate"
          >
            ⧉
          </button>
          <button
            v-if="selectedUserPreset"
            type="button"
            :class="[btnGhost, '!px-2']"
            :title="
              t(
                selectedUserPreset.favorite
                  ? 'preset.unfavorite'
                  : 'preset.favorite',
              )
            "
            @click="toggleFavoriteSelected"
          >
            {{ selectedUserPreset.favorite ? "★" : "☆" }}
          </button>
          <button
            v-if="selectedUserPreset"
            type="button"
            :class="[btnGhost, '!px-2']"
            :title="t('preset.rename')"
            @click="startPresetRename"
          >
            ✎
          </button>
          <button
            v-if="selectedUserPreset"
            type="button"
            :class="[btnGhost, '!px-2']"
            :title="t('preset.delete')"
            @click="deleteSelectedPreset"
          >
            ✕
          </button>
        </template>
        <template v-else>
          <input
            v-model="presetNameField"
            type="text"
            :placeholder="t('preset.saveName')"
            :class="[input, 'w-48']"
            @keyup.enter="confirmPresetEdit"
            @keyup.esc="cancelPresetEdit"
          />
          <button
            type="button"
            :class="btnPrimary"
            :disabled="
              !presetNameField.trim() ||
              (presetEdit === 'save' && paramsInvalid)
            "
            @click="confirmPresetEdit"
          >
            {{ t("preset.saveBtn") }}
          </button>
          <button type="button" :class="btnGhost" @click="cancelPresetEdit">
            {{ t("preset.cancel") }}
          </button>
        </template>
        <span v-if="presetError" class="text-xs text-danger">{{
          presetError
        }}</span>
      </div>

      <div class="flex flex-wrap items-end gap-4">
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.mode")
          }}</span>
          <select v-model="selectedMode" :class="input" data-demo="run-mode">
            <option v-for="mo in modes" :key="mo" :value="mo">
              {{ t("run.modes." + mo) }}
            </option>
          </select>
        </label>
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.format")
          }}</span>
          <select
            v-model="selectedFormat"
            :class="input"
            data-demo="run-format"
          >
            <option v-for="fmt in formats" :key="fmt" :value="fmt">
              {{ t("run.formats." + fmt) }}
            </option>
          </select>
        </label>
        <label v-if="isMosaicMode" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("mosaic.import.planLabel")
          }}</span>
          <select v-model.number="mosaicPlanId" :class="input">
            <option :value="0">{{ t("mosaic.import.noPlan") }}</option>
            <option v-for="p in mosaicStore.plans" :key="p.id" :value="p.id">
              {{ p.name }} ({{ p.grid.cols }}×{{ p.grid.rows }})
            </option>
          </select>
        </label>
        <p v-if="isMosaicMode" class="basis-full text-xs text-slate-400">
          {{ t("mosaic.import.folderHint") }}
        </p>
        <label v-if="s3.active" class="text-sm" :title="t('s3.storageHint')">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("s3.storage")
          }}</span>
          <select v-model="processMode" :class="input">
            <option value="local">{{ t("s3.storageLocal") }}</option>
            <option value="s3">{{ t("s3.storageS3") }}</option>
          </select>
        </label>
        <label
          v-if="s3.active && processMode === 's3' && isDeepskyFamily"
          class="flex items-center gap-2 self-end pb-2 text-sm"
          :title="t('s3.lowDiskHint')"
        >
          <input v-model="lowDisk" type="checkbox" :class="checkbox" />
          {{ t("s3.lowDisk") }}
        </label>
        <label v-if="isMilkyway" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.look")
          }}</span>
          <select v-model="look" :class="input">
            <option v-for="lk in looks" :key="lk" :value="lk">
              {{ t("run.looks." + lk) }}
            </option>
          </select>
        </label>
        <label v-if="isDeepskyFamily" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.palette")
          }}</span>
          <select v-model="palette" :class="input">
            <option
              v-for="opt in paletteOptions"
              :key="opt.value"
              :value="opt.value"
              :disabled="paletteMissing(opt.needs).length > 0"
              :title="
                paletteMissing(opt.needs).length
                  ? t('run.paletteNeeds', {
                      filters: paletteMissing(opt.needs).join(', '),
                    })
                  : ''
              "
            >
              {{ t("rerun.knobs.paletteOptions." + opt.value)
              }}{{
                paletteMissing(opt.needs).length
                  ? " — " +
                    t("run.paletteNeeds", {
                      filters: paletteMissing(opt.needs).join(", "),
                    })
                  : ""
              }}
            </option>
          </select>
        </label>
        <label v-if="isMilkyway" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.brightness")
          }}</span>
          <select v-model="brightness" :class="input">
            <option v-for="b in brightnesses" :key="b" :value="b">
              {{ t("run.brightnesses." + b) }}
            </option>
          </select>
        </label>
        <label v-if="isMilkyway" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.orientation")
          }}</span>
          <select v-model="orientation" :class="input">
            <option v-for="o in orientations" :key="o.label" :value="o.value">
              {{ t("run.orientations." + o.label) }}
            </option>
          </select>
          <span
            v-if="orientation && orientation !== 'auto'"
            class="mt-1 flex items-center gap-2 text-xs text-slate-500"
          >
            <input v-model="mirror" type="checkbox" :class="checkbox" />
            {{ t("run.mirror") }}
          </span>
        </label>
        <div class="flex flex-col gap-1 text-sm">
          <label class="flex items-center gap-2">
            <input
              v-model="colorCalibration"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-colorCalibration"
            />
            {{ t("run.colorCalibration") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="denoise"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-denoise"
            />
            {{ t("run.denoise") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="haExcludeStars"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-haExcludeStars"
            />
            {{ t("run.haExcludeStars") }}
          </label>
          <label
            v-if="isDeepskyFamily"
            class="flex items-center gap-2"
            :title="t('run.mosaicHint')"
          >
            <input
              v-model="mosaic"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-mosaic"
            />
            {{ t("run.mosaic") }}
          </label>
          <label
            v-if="isDeepskyFamily"
            class="flex items-center gap-2"
            :title="t('run.outputLuminanceHint')"
          >
            <input
              v-model="outputLuminance"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-outputLuminance"
            />
            {{ t("run.outputLuminance") }}
          </label>
          <label
            v-if="isDeepskyFamily"
            class="flex items-center gap-2"
            :title="t('run.outputMonoStackHint')"
          >
            <input
              v-model="outputMonoStack"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-outputMonoStack"
            />
            {{ t("run.outputMonoStack") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="dropWheelTransition"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-dropWheelTransition"
            />
            {{ t("run.dropTransition") }}
          </label>
          <label
            v-if="isPlanetary"
            class="flex items-center gap-2"
            :title="t('run.earthshineHint')"
          >
            <input
              v-model="earthshine"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-earthshine"
            />
            {{ t("run.earthshine") }}
          </label>
          <div v-if="isPlanetary" class="flex flex-col gap-1">
            <div class="flex items-center gap-2 text-xs text-slate-500">
              <label class="flex items-center gap-1">
                {{ t("run.alignPointsMinSize") }}
                <input
                  v-model.number="alignPointsMinPx"
                  type="number"
                  min="24"
                  step="1"
                  :placeholder="t('run.alignPointsAuto')"
                  :class="input"
                  class="!w-20 !py-1"
                />
              </label>
              <button
                type="button"
                :class="btnGhost"
                class="!px-2 !py-1"
                :disabled="
                  alignPointsBusy || !selectedPaths.length || paramsInvalid
                "
                :title="t('run.alignPointsHint')"
                data-demo="opt-alignPoints"
                @click="runAlignPointsEstimate"
              >
                {{
                  alignPointsBusy
                    ? t("run.alignPointsEstimating")
                    : t("run.alignPointsEstimate")
                }}
              </button>
            </div>
            <span v-if="alignPointsResult" class="text-xs text-slate-500">
              {{
                t("run.alignPointsResult", {
                  usable: alignPointsResult.usable_points,
                  n: alignPointsResult.per_axis,
                  cell: alignPointsResult.cell_px,
                  suggested: alignPointsResult.suggested_align_points,
                })
              }}
            </span>
            <span v-if="alignPointsError" class="text-xs text-danger">{{
              alignPointsError
            }}</span>
          </div>
          <div v-if="isDeepskyFamily" class="flex flex-col gap-1">
            <div class="flex items-center gap-2">
              <button
                type="button"
                :class="btnGhost"
                class="!px-2 !py-1"
                :disabled="setQaBusy || !selectedPaths.length"
                :title="t('setqa.hint')"
                data-demo="opt-setqa"
                @click="runSetQaAnalysis"
              >
                {{ setQaBusy ? t("setqa.checking") : t("setqa.button") }}
              </button>
              <button
                v-if="setQaReport && !setQaOpen"
                type="button"
                class="text-xs text-brand-500 underline"
                @click="setQaOpen = true"
              >
                {{ t("setqa.reopen") }}
              </button>
            </div>
            <span
              v-if="setQaReport && !setQaBusy"
              class="text-xs text-slate-500"
            >
              {{
                setQaReport.flagged
                  ? t("setqa.summary", { n: setQaReport.flagged })
                  : t("setqa.none")
              }}
            </span>
            <span v-if="setQaError" class="text-xs text-danger">{{
              setQaError
            }}</span>
            <div
              v-if="excludedSets.length"
              class="flex flex-wrap items-center gap-1 text-xs"
            >
              <span class="text-slate-500">
                {{ t("setqa.excludedOnRun", { n: excludedSets.length }) }}
              </span>
              <span
                v-for="id in excludedSets"
                :key="id"
                class="inline-flex items-center gap-1 rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-600 dark:text-amber-400"
              >
                {{ excludedSetLabel(id) }}
                <button
                  type="button"
                  class="hover:text-danger"
                  :title="t('setqa.reinclude')"
                  @click="excludedSets = excludedSets.filter((x) => x !== id)"
                >
                  ✕
                </button>
              </span>
              <button
                type="button"
                class="text-slate-500 underline"
                @click="excludedSets = []"
              >
                {{ t("setqa.clear") }}
              </button>
            </div>
          </div>
          <label
            v-if="calibForceApplies"
            class="flex items-center gap-2"
            :title="t('run.forceCalibrationHint')"
          >
            <input
              v-model="forceCalibration"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-forceCalibration"
            />
            {{ t("run.forceCalibration") }}
          </label>
          <label
            v-if="supportsSupervise"
            class="flex items-center gap-2"
            :title="t('run.superviseHint')"
          >
            <input
              v-model="supervise"
              type="checkbox"
              :class="checkbox"
              data-demo="opt-supervise"
            />
            {{ t("run.supervise") }}
          </label>
        </div>
        <button
          :class="btnPrimary"
          :disabled="!canRun"
          :title="mastersOnly ? t('run.buildMastersHint') : undefined"
          data-demo="run-pipeline"
          @click="runPipeline"
        >
          {{ t(mastersOnly ? "run.buildMasters" : "common.run") }}
        </button>
        <button
          :class="btnGhost"
          :disabled="!canRun"
          :title="t('run.addToQueueHint')"
          @click="queuePipeline"
        >
          {{ t("run.addToQueue") }}
        </button>
        <p
          v-if="mastersOnly"
          class="w-full text-xs text-slate-500 dark:text-slate-400"
        >
          {{ t("run.buildMastersHint") }}
        </p>
      </div>

      <!-- Optional calibration-frame folders (milkyway): point at separate dark/flat/bias dirs. -->
      <details v-if="isMilkyway" class="mt-3 text-sm">
        <summary class="cursor-pointer text-xs font-medium text-slate-500">
          {{ t("run.calibration") }}
        </summary>
        <div class="mt-2 grid gap-3 sm:grid-cols-3">
          <label class="text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.darks")
            }}</span>
            <input
              v-model="darkDir"
              type="text"
              :placeholder="t('run.calibPlaceholder')"
              :class="input"
            />
          </label>
          <label class="text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.flats")
            }}</span>
            <input
              v-model="flatDir"
              type="text"
              :placeholder="t('run.calibPlaceholder')"
              :class="input"
            />
          </label>
          <label class="text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.bias")
            }}</span>
            <input
              v-model="biasDir"
              type="text"
              :placeholder="t('run.calibPlaceholder')"
              :class="input"
            />
          </label>
        </div>
      </details>

      <!-- Advanced AI parameters: free-text goal + fine knob overrides (JSON), forwarded on the run.
           Opening it prefills the JSON with the selected mode's effective knobs (checkbox-owned ones
           excluded). -->
      <details
        class="mt-3 text-sm"
        :open="advancedOpen"
        @toggle="onAdvancedToggle"
      >
        <summary class="cursor-pointer text-xs font-medium text-slate-500">
          {{ t("run.advancedParams") }}
        </summary>

        <!-- Imaging target (deepsky family): seeds the plate-solve so SPCC colour calibration can run
             when the FITS headers / folder names can't identify the field. -->
        <section v-if="isDeepskyFamily" class="mt-3">
          <label class="block text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.target")
            }}</span>
            <input
              v-model="target"
              type="text"
              :placeholder="t('run.targetHint')"
              :class="input"
              data-demo="run-target"
            />
          </label>
        </section>

        <!-- AI guidance: a plain-language objective the finish supervisor carries (only used when the
             "Supervise finish" AI agent is on). -->
        <section class="mt-3">
          <h4
            class="text-xs font-semibold uppercase tracking-wide text-slate-500"
          >
            {{ t("run.aiSection") }}
          </h4>
          <p class="mt-0.5 text-xs text-slate-400">
            {{ t("run.aiSectionHint") }}
          </p>
          <label class="mt-2 block text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.goal")
            }}</span>
            <input
              v-model="goal"
              type="text"
              :placeholder="t('run.goalHint')"
              :class="input"
              data-demo="run-goal"
            />
          </label>
        </section>

        <!-- Pipeline parameters: fine tunable-knob overrides applied to EVERY run (AI or not), prefilled
             with the selected mode's effective knobs. -->
        <section
          class="mt-4 border-t border-slate-200 pt-3 dark:border-slate-700"
        >
          <h4
            class="text-xs font-semibold uppercase tracking-wide text-slate-500"
          >
            {{ t("run.pipelineSection") }}
          </h4>
          <p class="mt-0.5 text-xs text-slate-400">
            {{ t("run.pipelineSectionHint") }}
          </p>
          <label class="mt-2 block text-sm">
            <span
              class="mb-1 flex items-center justify-between gap-2 text-xs font-medium text-slate-500"
            >
              {{ t("run.paramsJson") }}
              <button
                type="button"
                class="font-normal text-brand-500 hover:text-brand-700 hover:underline dark:text-brand-300 dark:hover:text-brand-100"
                @click="resetParams"
              >
                {{ t("run.paramsReset") }}
              </button>
            </span>
            <textarea
              v-model="paramsText"
              rows="4"
              spellcheck="false"
              :placeholder="t('run.paramsPlaceholder')"
              :class="[
                input,
                'h-24 font-mono text-xs',
                paramsInvalid
                  ? '!border-danger-500 focus:!border-danger-500 focus:!ring-danger-500'
                  : '',
              ]"
              data-demo="run-params"
              @input="paramsDirty = true"
            />
            <span v-if="paramsInvalid" class="mt-1 block text-xs text-danger">
              {{ t("run.paramsInvalid") }}
            </span>
            <span v-else class="mt-1 block text-xs text-slate-400">
              {{ t("run.paramsCheckboxNote") }}
            </span>
          </label>
          <!-- Per-parameter reference for the JSON above: every knob this mode exposes, the ones set in
               the JSON highlighted with their live value. -->
          <ParamGlossary
            :mode="selectedMode"
            :params="runParams"
            :defaults="modeDefaults"
            :ranges="modeRanges"
            interactive
            :disabled="paramsInvalid"
            @toggle="toggleParam"
            @nudge="nudgeParam"
          />
        </section>
      </details>
      <p v-if="!selectedPaths.length" class="mt-2 text-xs text-slate-400">
        {{ t("import.selectCapture") }}
      </p>
      <p
        v-if="queuedCount"
        class="mt-2 text-xs text-success-600 dark:text-success-300"
      >
        {{ t("import.queuedToast", { n: queuedCount }) }}
        <router-link
          :to="{ name: 'jobs' }"
          class="font-medium underline hover:text-success-700 dark:hover:text-success-200"
        >
          {{ t("import.viewQueue") }}
        </router-link>
      </p>
    </div>

    <div v-if="inv" class="space-y-6">
      <div class="flex flex-wrap gap-3">
        <div
          v-for="(n, type) in counts"
          :key="type"
          :class="[
            card,
            frameTypeCardClass(String(type)),
            'min-w-[7rem] text-center',
          ]"
        >
          <div
            class="text-2xl font-bold"
            :class="frameTypeAccentClass(String(type))"
          >
            {{ n }}
          </div>
          <div class="text-xs uppercase tracking-wide text-slate-500">
            {{ type }}
          </div>
        </div>
      </div>

      <section v-if="lightRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.lightSets") }}</h2>
        <GenericTable
          :columns="lightColumns"
          :rows="lightRows"
          max-height="20rem"
        >
          <template #cell-filter="{ value }">
            <FilterChip v-if="value" :filter="String(value)" />
            <span v-else class="text-slate-400">—</span>
          </template>
        </GenericTable>
      </section>

      <section v-if="calibRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.calibSets") }}</h2>
        <GenericTable
          :columns="calibColumns"
          :rows="calibRows"
          max-height="20rem"
        >
          <template #cell-filter="{ value }">
            <FilterChip v-if="value" :filter="String(value)" />
            <span v-else class="text-slate-400">—</span>
          </template>
        </GenericTable>
      </section>

      <section v-if="fileRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.files") }}</h2>
        <GenericTable
          :columns="fileColumns"
          :rows="fileRows"
          max-height="28rem"
        >
          <template #cell-filter="{ value }">
            <FilterChip v-if="value" :filter="String(value)" />
            <span v-else class="text-slate-400">—</span>
          </template>
          <template #cell-view="{ row }">
            <FilePreviewButton :path="String(row.path)" />
          </template>
        </GenericTable>
      </section>

      <section v-if="inv.warnings && inv.warnings.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.warnings") }}</h2>
        <ul class="space-y-1">
          <li
            v-for="(w, i) in inv.warnings"
            :key="i"
            class="text-sm text-warning"
          >
            ⚠ {{ w }}
          </li>
        </ul>
      </section>
    </div>

    <p v-else-if="!browseStore.loading" class="text-sm text-slate-400">
      {{ t("import.noData") }}
    </p>

    <SetArtifactsModal
      v-if="setQaOpen && setQaReport"
      :report="setQaReport"
      :initial="excludedSets"
      @apply="applySetExclusions"
      @close="setQaOpen = false"
    />
  </div>
</template>
