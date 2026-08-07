import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { apiDelete, apiGet, apiPost, apiPut } from "@/services/api";
import { useSkyStore } from "@/stores/sky";
import type {
  MosaicCaptureTarget,
  MosaicFilterProgress,
  MosaicPlanRow,
  MosaicPreview,
  MosaicRequestBody,
  MosaicTileStatus,
  SkySearchResult,
  SkyTarget,
  StarfieldStar,
} from "@/types";

// Mosaic planner + capture assistant state. The geometry itself is server-computed
// (POST /api/mosaic/preview — the Go mosaicplan package is the single source of truth); this store
// owns the draft inputs, the debounced preview, saved-plan CRUD and the per-tile capture progress
// (optimistic with revert, mirroring the goto store's accept/undo model).

const ACTIVE_KEY = "astrostack.mosaic.activePlanId";
const PREVIEW_DEBOUNCE_MS = 300;

// MosaicDraft is the planner form. Only the target NAME is required for a catalogued object — the
// server resolves coordinates/size/PA; the custom fields exist for un-catalogued centers.
export interface MosaicDraft {
  targetName: string;
  customRaDeg?: number;
  customDecDeg?: number;
  customSizeArcmin?: number;
  customSizeMinorArcmin?: number;
  customObjectPaDeg?: number;
  // Hand-framing: where the grid sits when the user has dragged it off the object. Both set or
  // both absent.
  centerRaDeg?: number;
  centerDecDeg?: number;
  setupId: string; // "" = the current Tonight equipment (server echo), else an EquipmentSetup id
  // optics overrides both of the above: the planner's own editable rig, for a one-off setup the user
  // hasn't saved (or doesn't want to). Cleared by "use the selected setup".
  optics?: MosaicRequestBody["optics"];
  overlapPct: number;
  marginArcmin: number;
  cameraPaDeg: number;
  rowsOverride: number;
  colsOverride: number;
}

function defaultDraft(): MosaicDraft {
  return {
    targetName: "",
    setupId: "",
    overlapPct: 20,
    marginArcmin: 10,
    cameraPaDeg: 0,
    rowsOverride: 0,
    colsOverride: 0,
  };
}

function loadActiveId(): number | null {
  try {
    const raw = localStorage.getItem(ACTIVE_KEY);
    return raw ? Number(raw) : null;
  } catch {
    return null;
  }
}

export const useMosaicStore = defineStore("mosaic", () => {
  const sky = useSkyStore();

  const draft = ref<MosaicDraft>(defaultDraft());
  const preview = ref<MosaicPreview | null>(null);
  const previewLoading = ref(false);
  const previewError = ref("");

  const plans = ref<MosaicPlanRow[]>([]);
  const plansLoaded = ref(false);
  let plansInflight: Promise<void> | null = null;

  const activePlan = ref<MosaicPlanRow | null>(null);
  const activePlanId = ref<number | null>(loadActiveId());
  const selectedTileIndex = ref<number | null>(null);
  const busy = ref(false);

  const starfields = ref(new Map<string, StarfieldStar[]>());

  function persistActiveId(id: number | null) {
    activePlanId.value = id;
    try {
      if (id === null) localStorage.removeItem(ACTIVE_KEY);
      else localStorage.setItem(ACTIVE_KEY, String(id));
    } catch {
      // private-mode quota — the id is a convenience only
    }
  }

  // requestBody assembles the wire request from the draft: optics from the chosen saved setup
  // (or the Tonight echo), site from the Tonight echo; anything unknown is omitted so the engine's
  // configured defaults apply.
  function requestBody(): MosaicRequestBody {
    const d = draft.value;
    const body: MosaicRequestBody = {
      overlap_frac: d.overlapPct / 100,
      margin_arcmin: d.marginArcmin,
      camera_pa_deg: d.cameraPaDeg,
      rows_override: d.rowsOverride,
      cols_override: d.colsOverride,
    };
    if (d.targetName) body.target_name = d.targetName;
    if (d.customRaDeg !== undefined && d.customDecDeg !== undefined) {
      body.ra_deg = d.customRaDeg;
      body.dec_deg = d.customDecDeg;
    }
    if (d.customSizeArcmin !== undefined) body.size_arcmin = d.customSizeArcmin;
    if (d.customSizeMinorArcmin !== undefined)
      body.size_minor_arcmin = d.customSizeMinorArcmin;
    if (d.customObjectPaDeg !== undefined)
      body.object_pa_deg = d.customObjectPaDeg;
    if (d.centerRaDeg !== undefined && d.centerDecDeg !== undefined) {
      body.center_ra_deg = d.centerRaDeg;
      body.center_dec_deg = d.centerDecDeg;
    }

    const setup = sky.equipmentSetups.find((s) => s.id === d.setupId);
    if (d.optics && Object.values(d.optics).some((v) => v)) {
      body.optics = { ...d.optics };
    } else if (setup) {
      body.optics = {
        focal_mm: setup.focal_mm,
        aperture_mm: setup.aperture_mm,
        pixel_um: setup.pixel_um,
        sensor_w_px: setup.sensor_w,
        sensor_h_px: setup.sensor_h,
        barlow_x: setup.barlow,
        reducer_x: setup.reducer,
      };
    } else if (sky.query?.equipment) {
      const eq = sky.query.equipment;
      body.optics = {
        focal_mm: eq.focal_mm,
        aperture_mm: eq.aperture_mm,
        pixel_um: eq.pixel_um,
        sensor_w_px: eq.sensor_w_px,
        sensor_h_px: eq.sensor_h_px,
        barlow_x: eq.barlow_x,
        reducer_x: eq.reducer_x,
      };
    }
    if (sky.query?.location) {
      body.lat = sky.query.location.lat;
      body.lon = sky.query.location.lon;
    }
    return body;
  }

  // keptRig carries the chosen rig across a target change — the telescope doesn't change when the
  // user picks a different object, and silently reverting to the default optics would resize every
  // tile without saying so.
  function keptRig(): Pick<MosaicDraft, "setupId" | "optics"> {
    return { setupId: draft.value.setupId, optics: draft.value.optics };
  }

  // seedFromObject points the draft at a Tonight object (custom overrides cleared — the server
  // resolves everything from the catalogue) and pre-aims the camera along the object when its
  // position angle is known.
  function seedFromObject(target: SkyTarget | string) {
    const name = typeof target === "string" ? target : target.name;
    const pa =
      typeof target === "string" ? undefined : target.position_angle_deg;
    draft.value = {
      ...defaultDraft(),
      ...keptRig(),
      targetName: name,
      cameraPaDeg:
        pa !== undefined ? Math.round(((pa + 90) % 360) * 10) / 10 : 0,
    };
    void computePreview().then(() => {
      // Seeded by name only: once the server resolved the object's position angle, pre-aim the
      // camera along it (the fewest-tiles orientation) — unless the user already turned the knob.
      const resolvedPa = preview.value?.query.object_pa_deg;
      if (
        pa === undefined &&
        resolvedPa !== undefined &&
        draft.value.targetName === name &&
        draft.value.cameraPaDeg === 0
      ) {
        draft.value.cameraPaDeg =
          Math.round(((resolvedPa + 90) % 360) * 10) / 10;
      }
    });
  }

  // seedFromSearch points the draft at a catalogue hit. Unlike seedFromObject it carries the size and
  // position angle it already has, so the grid is right on the FIRST preview instead of after a
  // round-trip — and an object with no catalogued size still lands (the server warns size_unknown).
  function seedFromSearch(res: SkySearchResult) {
    draft.value = {
      ...defaultDraft(),
      ...keptRig(),
      targetName: res.name,
      cameraPaDeg:
        res.position_angle_deg !== undefined
          ? Math.round(((res.position_angle_deg + 90) % 360) * 10) / 10
          : 0,
    };
    void computePreview();
  }

  const searchResults = ref<SkySearchResult[]>([]);
  const searchLoading = ref(false);
  let searchAbort: AbortController | null = null;

  // searchTargets runs the catalogue type-ahead. Each keystroke aborts the previous request, so a
  // slow response can never overwrite a newer one; a blank query just clears the list.
  async function searchTargets(query: string): Promise<void> {
    searchAbort?.abort();
    const q = query.trim();
    if (!q) {
      searchResults.value = [];
      searchLoading.value = false;
      return;
    }
    const ctl = new AbortController();
    searchAbort = ctl;
    searchLoading.value = true;
    try {
      const res = await apiGet<{ results: SkySearchResult[] }>(
        `/api/sky/search?q=${encodeURIComponent(q)}&limit=20`,
        ctl.signal,
      );
      searchResults.value = res.results ?? [];
    } catch {
      if (!ctl.signal.aborted) searchResults.value = [];
    } finally {
      if (searchAbort === ctl) {
        searchLoading.value = false;
        searchAbort = null;
      }
    }
  }

  let previewTimer: number | undefined;
  let previewAbort: AbortController | null = null;

  // computePreview recomputes the tile grid, debounced and cancellable — slider drags coalesce
  // into one request and a stale response can never overwrite a newer one.
  function computePreview(): Promise<void> {
    window.clearTimeout(previewTimer);
    return new Promise((resolve) => {
      previewTimer = window.setTimeout(async () => {
        previewAbort?.abort();
        const ctl = new AbortController();
        previewAbort = ctl;
        previewLoading.value = true;
        previewError.value = "";
        try {
          preview.value = await apiPost<MosaicPreview>(
            "/api/mosaic/preview",
            requestBody(),
            ctl.signal,
          );
          if (
            selectedTileIndex.value !== null &&
            selectedTileIndex.value >= (preview.value?.tiles.length ?? 0)
          )
            selectedTileIndex.value = null;
        } catch (e) {
          if (!ctl.signal.aborted) {
            preview.value = null;
            previewError.value = e instanceof Error ? e.message : String(e);
          }
        } finally {
          if (previewAbort === ctl) {
            previewLoading.value = false;
            previewAbort = null;
          }
          resolve();
        }
      }, PREVIEW_DEBOUNCE_MS);
    });
  }

  async function listPlans(force = false): Promise<void> {
    if (plansLoaded.value && !force) return;
    if (plansInflight) return plansInflight;
    plansInflight = (async () => {
      try {
        const res = await apiGet<{ plans: MosaicPlanRow[] }>(
          "/api/mosaic/plans",
        );
        plans.value = res.plans;
        plansLoaded.value = true;
      } finally {
        plansInflight = null;
      }
    })();
    return plansInflight;
  }

  async function loadPlan(id: number, force = false): Promise<MosaicPlanRow> {
    if (!force && activePlan.value?.id === id) return activePlan.value;
    const res = await apiGet<{ plan: MosaicPlanRow }>(
      `/api/mosaic/plans/${id}`,
    );
    activePlan.value = res.plan;
    persistActiveId(id);
    return res.plan;
  }

  // draftFromPlan re-fills the planner form from a saved plan's resolved request snapshot.
  function draftFromPlan(plan: MosaicPlanRow) {
    const rq = plan.request;
    draft.value = {
      targetName: plan.object_name,
      customRaDeg: plan.object_name ? undefined : rq.ra_deg,
      customDecDeg: plan.object_name ? undefined : rq.dec_deg,
      customSizeArcmin: plan.object_name ? undefined : rq.size_arcmin,
      customSizeMinorArcmin: plan.object_name
        ? undefined
        : rq.size_minor_arcmin,
      customObjectPaDeg:
        !plan.object_name && rq.has_object_pa ? rq.object_pa_deg : undefined,
      centerRaDeg: rq.has_center ? rq.center_ra_deg : undefined,
      centerDecDeg: rq.has_center ? rq.center_dec_deg : undefined,
      setupId: "",
      // The plan snapshots the optics it was computed with; restoring them means re-opening a plan
      // can never silently re-plan it against whatever rig is selected today.
      optics: rq.optics ? { ...rq.optics } : undefined,
      overlapPct: Math.round(rq.overlap_frac * 100),
      marginArcmin: rq.margin_arcmin,
      cameraPaDeg: rq.camera_pa_deg,
      rowsOverride: rq.rows_override,
      colsOverride: rq.cols_override,
    };
    preview.value = {
      query: previewEchoOfPlan(plan),
      grid: plan.grid,
      tiles: plan.tiles,
    };
  }

  function previewEchoOfPlan(plan: MosaicPlanRow): MosaicPreview["query"] {
    const rq = plan.request;
    return {
      target: plan.object_name,
      ra_deg: rq.ra_deg,
      dec_deg: rq.dec_deg,
      size_arcmin: rq.size_arcmin,
      size_minor_arcmin: rq.size_minor_arcmin,
      object_pa_deg: rq.has_object_pa ? rq.object_pa_deg : undefined,
      center_ra_deg: rq.has_center ? rq.center_ra_deg : rq.ra_deg,
      center_dec_deg: rq.has_center ? rq.center_dec_deg : rq.dec_deg,
      center_moved: rq.has_center,
      fov_w_deg: plan.grid.tile_w_deg,
      fov_h_deg: plan.grid.tile_h_deg,
      image_scale_arcsec_px: 0,
      margin_arcmin: rq.margin_arcmin,
      lat: rq.lat,
      lon: rq.lon,
      at_utc_ms: Date.parse(rq.at) || Date.now(),
    };
  }

  async function savePlan(name: string): Promise<MosaicPlanRow> {
    busy.value = true;
    try {
      const res = await apiPost<{ plan: MosaicPlanRow }>("/api/mosaic/plans", {
        name,
        request: requestBody(),
      });
      activePlan.value = res.plan;
      persistActiveId(res.plan.id);
      await listPlans(true);
      return res.plan;
    } finally {
      busy.value = false;
    }
  }

  // updatePlanGeometry re-plans the active plan from the current draft; returns true when the
  // server reset the tile progress (geometry changed) so the UI can toast it.
  async function updatePlanGeometry(): Promise<boolean> {
    if (!activePlan.value) return false;
    busy.value = true;
    try {
      const res = await apiPut<{
        plan: MosaicPlanRow;
        statuses_reset: boolean;
      }>(`/api/mosaic/plans/${activePlan.value.id}`, {
        request: requestBody(),
      });
      activePlan.value = res.plan;
      await listPlans(true);
      return res.statuses_reset;
    } finally {
      busy.value = false;
    }
  }

  async function renamePlan(id: number, name: string): Promise<void> {
    await apiPut(`/api/mosaic/plans/${id}`, { name });
    if (activePlan.value?.id === id) activePlan.value.name = name;
    await listPlans(true);
  }

  async function duplicatePlan(
    id: number,
    name: string,
  ): Promise<MosaicPlanRow> {
    const { plan } = await apiGet<{ plan: MosaicPlanRow }>(
      `/api/mosaic/plans/${id}`,
    );
    const rq = plan.request;
    const body: MosaicRequestBody = {
      target_name: plan.object_name || undefined,
      ra_deg: rq.ra_deg,
      dec_deg: rq.dec_deg,
      size_arcmin: rq.size_arcmin,
      size_minor_arcmin: rq.size_minor_arcmin,
      object_pa_deg: rq.has_object_pa ? rq.object_pa_deg : undefined,
      center_ra_deg: rq.has_center ? rq.center_ra_deg : undefined,
      center_dec_deg: rq.has_center ? rq.center_dec_deg : undefined,
      optics: rq.optics,
      overlap_frac: rq.overlap_frac,
      margin_arcmin: rq.margin_arcmin,
      camera_pa_deg: rq.camera_pa_deg,
      rows_override: rq.rows_override,
      cols_override: rq.cols_override,
      lat: rq.lat,
      lon: rq.lon,
    };
    const res = await apiPost<{ plan: MosaicPlanRow }>("/api/mosaic/plans", {
      name,
      request: body,
    });
    await listPlans(true);
    return res.plan;
  }

  // deselectPlan drops the "this is the plan being captured" marker without touching the plan.
  //
  // It matters because activePlanId outlives a reload — it lives in localStorage so a phone at the
  // scope resumes exactly where it stood. That is right when you come back to the same mosaic and
  // wrong the moment you start planning a different object: the Capture tab would go on offering
  // last night's target, and the first thing it offers to do with a target is slew a telescope at it.
  function deselectPlan(): void {
    activePlan.value = null;
    persistActiveId(null);
  }

  async function deletePlan(id: number): Promise<void> {
    await apiDelete(`/api/mosaic/plans/${id}`);
    if (activePlan.value?.id === id) {
      activePlan.value = null;
      persistActiveId(null);
    }
    await listPlans(true);
  }

  // setTileStatus is optimistic: the chip flips immediately, reverts on failure (the phone at the
  // scope may drop the network — the card shows a retry).
  async function setTileStatus(
    index: number,
    status: MosaicTileStatus,
  ): Promise<void> {
    const plan = activePlan.value;
    if (!plan) return;
    const key = String(index);
    const prev = plan.tile_status[key];
    if (status === "pending") delete plan.tile_status[key];
    else plan.tile_status[key] = status;
    try {
      await apiPut(`/api/mosaic/plans/${plan.id}/tiles/${index}`, { status });
    } catch (e) {
      if (prev === undefined) delete plan.tile_status[key];
      else plan.tile_status[key] = prev;
      throw e;
    }
  }

  // --- Capture progress across nights ------------------------------------------------------------

  // setCaptureTargets stores the per-filter goal for every tile. Targets are what let the app say
  // "this tile is finished" from the frames on disk instead of asking the user to remember.
  async function setCaptureTargets(
    targets: MosaicCaptureTarget[],
  ): Promise<void> {
    const plan = activePlan.value;
    if (!plan) return;
    const res = await apiPut<{ plan: MosaicPlanRow }>(
      `/api/mosaic/plans/${plan.id}`,
      { capture_targets: targets },
    );
    activePlan.value = res.plan;
  }

  // reconcile re-counts the frames actually sitting in the capture folder and refreshes the plan's
  // progress (also auto-marking tiles whose targets are met). Returns how many tiles it marked.
  async function reconcile(path: string): Promise<number> {
    const plan = activePlan.value;
    if (!plan) return 0;
    busy.value = true;
    try {
      const res = await apiPost<{
        plan: MosaicPlanRow;
        auto_marked: number;
      }>(`/api/mosaic/plans/${plan.id}/reconcile`, { path });
      activePlan.value = res.plan;
      await listPlans(true);
      return res.auto_marked;
    } finally {
      busy.value = false;
    }
  }

  // progressFor returns one tile's per-filter tallies (empty when nothing has been shot there).
  function progressFor(folder: string): Record<string, MosaicFilterProgress> {
    return activePlan.value?.tile_progress?.[folder] ?? {};
  }

  // filtersRemaining lists the filters a tile still owes frames for, in target order — mirrors
  // internal/mosaic.RemainingFilters so the UI and the engine agree on "what's left".
  function filtersRemaining(folder: string): string[] {
    const targets = activePlan.value?.capture_targets ?? [];
    const done = progressFor(folder);
    return targets
      .filter(
        (tgt) => tgt.frames > 0 && (done[tgt.filter]?.frames ?? 0) < tgt.frames,
      )
      .map((tgt) => tgt.filter);
  }

  // tileFraction is how much of a tile's total goal is in the can, 0–1 (null with no targets).
  function tileFraction(folder: string): number | null {
    const targets = activePlan.value?.capture_targets ?? [];
    const want = targets.reduce((n, tgt) => n + Math.max(0, tgt.frames), 0);
    if (!want) return null;
    const done = progressFor(folder);
    const got = targets.reduce(
      (n, tgt) => n + Math.min(tgt.frames, done[tgt.filter]?.frames ?? 0),
      0,
    );
    return got / want;
  }

  // nextIncompleteTile is the "resume where I stopped" answer: the first tile in capture order that
  // is neither ticked nor complete against its targets.
  const nextIncompleteTile = computed(() => {
    const plan = activePlan.value;
    if (!plan) return null;
    const ordered = [...plan.tiles].sort((a, b) => a.order - b.order);
    for (const tile of ordered) {
      const status = plan.tile_status[String(tile.index)];
      if (status === "captured" || status === "skipped") continue;
      return tile;
    }
    return null;
  });

  async function setOrientationDone(done: boolean): Promise<void> {
    const plan = activePlan.value;
    if (!plan) return;
    const prev = plan.orientation_done;
    plan.orientation_done = done;
    try {
      await apiPut(`/api/mosaic/plans/${plan.id}`, { orientation_done: done });
    } catch (e) {
      plan.orientation_done = prev;
      throw e;
    }
  }

  // fetchStarfield caches deepstars cutouts by rounded center+fov (the assistant re-opens the same
  // charts repeatedly; the sky doesn't move at this precision).
  async function fetchStarfield(
    raDeg: number,
    decDeg: number,
    fovDeg: number,
  ): Promise<StarfieldStar[]> {
    const key = `${raDeg.toFixed(1)}:${decDeg.toFixed(1)}:${fovDeg.toFixed(1)}`;
    const hit = starfields.value.get(key);
    if (hit) return hit;
    const res = await apiGet<{ stars: StarfieldStar[] }>(
      `/api/sky/starfield?ra=${raDeg}&dec=${decDeg}&fov=${fovDeg}`,
    );
    starfields.value.set(key, res.stars);
    return res.stars;
  }

  function planProgress(plan: MosaicPlanRow): {
    captured: number;
    skipped: number;
    total: number;
  } {
    let captured = 0;
    let skipped = 0;
    for (const v of Object.values(plan.tile_status)) {
      if (v === "captured") captured++;
      else if (v === "skipped") skipped++;
    }
    return { captured, skipped, total: plan.tiles.length };
  }

  const progress = computed(() =>
    activePlan.value ? planProgress(activePlan.value) : null,
  );

  // inProgressPlan powers the GoTo "continue mosaic capture" card: the most recently touched plan
  // with some — but not all — tiles done.
  const inProgressPlan = computed(() => {
    for (const plan of plans.value) {
      const p = planProgress(plan);
      const done = p.captured + p.skipped;
      if (done > 0 && done < p.total) return plan;
    }
    return null;
  });

  return {
    draft,
    preview,
    previewLoading,
    previewError,
    plans,
    plansLoaded,
    activePlan,
    activePlanId,
    selectedTileIndex,
    busy,
    searchResults,
    searchLoading,
    progress,
    inProgressPlan,
    requestBody,
    seedFromObject,
    seedFromSearch,
    searchTargets,
    computePreview,
    listPlans,
    loadPlan,
    draftFromPlan,
    savePlan,
    updatePlanGeometry,
    renamePlan,
    duplicatePlan,
    deselectPlan,
    deletePlan,
    setTileStatus,
    setCaptureTargets,
    reconcile,
    progressFor,
    filtersRemaining,
    tileFraction,
    nextIncompleteTile,
    setOrientationDone,
    fetchStarfield,
    planProgress,
  };
});
