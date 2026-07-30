import { defineStore } from "pinia";
import { computed, ref } from "vue";
import {
  apiDelete,
  apiGet,
  apiPost,
  apiPut,
  previewUrl,
  withS3,
} from "@/services/api";
import { PROCESSED_GROUP_COLORS } from "@/constants/colors";
import { baseName } from "@/utils/format";
import { useS3Store } from "@/stores/s3";
import { fetchPreviewBuffer } from "@/utils/previewbuf";
import type {
  BrowseEntry,
  Inventory,
  PreviewImage,
  ProcessedFolder,
  ProcessedGroup,
  ProcessingHistoryEntry,
  SavedSelectionInfo,
} from "@/types";

// All API calls for directory browsing and inspection live here; components dispatch actions.
export const useBrowseStore = defineStore("browse", () => {
  const path = ref("");
  const entries = ref<BrowseEntry[]>([]);
  const inventory = ref<Inventory | null>(null);
  const loading = ref(false);
  const error = ref("");
  // Cross-location multi-select: folders the user checked across different parent dirs, kept as full
  // entries (name + path) so pills can render even after navigating away. Persists across browse().
  const selected = ref<BrowseEntry[]>([]);

  function isSelected(p: string): boolean {
    return selected.value.some((e) => e.path === p);
  }
  // toggleSelected adds a folder to the selection, or removes it if already present (idempotent by path).
  function toggleSelected(entry: BrowseEntry) {
    const i = selected.value.findIndex((e) => e.path === entry.path);
    if (i >= 0) selected.value.splice(i, 1);
    else selected.value.push(entry);
  }
  function clearSelected() {
    selected.value = [];
  }

  // Past processings: which folders have been part of a job, and how to group folders processed
  // together. Loaded once and exposed as a path→info map for the browser annotations.
  const processedGroups = ref<ProcessedGroup[]>([]);
  // Saved selections (named/starred folder-sets) riding along the same report; joined onto the
  // history rows by backend-computed signature.
  const savedSelections = ref<SavedSelectionInfo[]>([]);

  async function loadProcessed() {
    try {
      const data = await apiGet<{
        groups: ProcessedGroup[];
        selections?: SavedSelectionInfo[];
      }>(withS3("/api/processed"));
      processedGroups.value = data.groups ?? [];
      savedSelections.value = data.selections ?? [];
    } catch {
      processedGroups.value = [];
      savedSelections.value = [];
    }
  }

  // saveSelection names a history entry's folder-set (upsert by signature server-side); favorite,
  // when set, stars it in the same call (starring an unnamed row routes through naming).
  async function saveSelection(
    name: string,
    entry: ProcessingHistoryEntry,
    favorite?: boolean,
  ): Promise<void> {
    await apiPost("/api/selections", {
      name,
      paths: entry.paths.map((p) => p.path),
      mode: entry.mode,
      format: entry.format,
      favorite,
    });
    await loadProcessed();
  }

  async function renameSelection(id: number, name: string): Promise<void> {
    await apiPut(`/api/selections/${id}`, { name });
    await loadProcessed();
  }

  async function setSelectionFavorite(
    id: number,
    favorite: boolean,
  ): Promise<void> {
    await apiPut(`/api/selections/${id}`, { favorite });
    await loadProcessed();
  }

  async function deleteSelection(id: number): Promise<void> {
    await apiDelete(`/api/selections/${id}`);
    await loadProcessed();
  }

  // loadProcessedFor fetches just ONE job's processed group (GET /api/processed?job_id=…) and merges it into
  // processedGroups (replace-or-append by job_id). The task-detail page needs only its own group (to gate the
  // "Remove local files" action), so it must not pull the whole recent window like loadProcessed does.
  async function loadProcessedFor(jobId: number) {
    try {
      const data = await apiGet<{ groups: ProcessedGroup[] }>(
        withS3(`/api/processed?job_id=${jobId}`),
      );
      const g = (data.groups ?? []).find((x) => x.job_id === jobId);
      const rest = processedGroups.value.filter((x) => x.job_id !== jobId);
      processedGroups.value = g ? [...rest, g] : rest;
    } catch {
      // leave any previously-loaded groups intact; the action just won't light up
    }
  }

  // path → processing info. Folders from one multi-folder job share a colour (keyed by job id); the
  // most-recent job is each folder's representative, and `runs` counts how many jobs included it.
  const processedByPath = computed<Map<string, ProcessedFolder>>(() => {
    const map = new Map<string, ProcessedFolder>();
    const recent = [...processedGroups.value].sort(
      (a, b) => (b.created_at_ms || 0) - (a.created_at_ms || 0),
    );
    for (const g of recent) {
      const multi = (g.paths?.length ?? 0) > 1;
      const color = multi
        ? PROCESSED_GROUP_COLORS[g.job_id % PROCESSED_GROUP_COLORS.length]
        : undefined;
      for (const p of g.paths ?? []) {
        // Key case-insensitively: capture filesystems are usually case-insensitive (macOS/Windows),
        // and a job may have recorded a differently-cased path than the on-disk name the browser shows.
        const key = p.path.toLowerCase();
        const existing = map.get(key);
        if (existing) {
          existing.runs++;
          continue;
        }
        map.set(key, {
          jobId: g.job_id,
          object: g.object,
          kind: g.kind,
          runs: 1,
          groupColor: color,
          groupSize: multi ? g.paths.length : undefined,
        });
      }
    }
    return map;
  });

  // localSignature is the fallback folder-set key for an old backend that doesn't send `signature`.
  // The backend key (store.SelectionSignature) is authoritative — same recipe, but computed once
  // server-side so Go and JS normalization can never drift.
  function localSignature(paths: { path: string }[]): string {
    return paths
      .map((p) => p.path.toLowerCase())
      .sort()
      .join("|");
  }

  // processingHistory de-duplicates past jobs by their folder-set so the Import view can offer each
  // unique selection for re-running. Most-recent first, favorites pinned on top; runs counts how many
  // jobs used that exact set. Saved selections join by signature; a saved selection whose jobs aged
  // out of the window is appended as an orphan row (jobId 0) so a named set never disappears.
  const processingHistory = computed<ProcessingHistoryEntry[]>(() => {
    const recent = [...processedGroups.value].sort(
      (a, b) => (b.created_at_ms || 0) - (a.created_at_ms || 0),
    );
    const selBySig = new Map(
      savedSelections.value.map((s) => [s.signature, s]),
    );
    const bySig = new Map<string, ProcessingHistoryEntry>();
    for (const g of recent) {
      const paths = g.paths ?? [];
      if (!paths.length) continue;
      const sig = g.signature ?? localSignature(paths);
      const existing = bySig.get(sig);
      if (existing) {
        existing.runs++;
        continue;
      }
      const sel = selBySig.get(sig);
      bySig.set(sig, {
        jobId: g.job_id,
        object: g.object,
        mode: g.mode,
        format: g.format,
        status: g.status,
        createdAtMs: g.created_at_ms,
        runs: 1,
        signature: sig,
        selection: sel
          ? { id: sel.id, name: sel.name, favorite: sel.favorite }
          : undefined,
        paths,
      });
    }
    for (const sel of savedSelections.value) {
      if (bySig.has(sel.signature)) continue;
      bySig.set(sel.signature, {
        jobId: 0,
        mode: sel.mode || undefined,
        format: sel.format || undefined,
        status: "",
        createdAtMs: sel.updated_at_ms,
        runs: 0,
        signature: sel.signature,
        selection: { id: sel.id, name: sel.name, favorite: sel.favorite },
        paths: sel.paths,
      });
    }
    // Stable pin: favorites first, preserving the newest-first order within each band.
    const rows = [...bySig.values()];
    const fav = rows.filter((r) => r.selection?.favorite);
    return [...fav, ...rows.filter((r) => !r.selection?.favorite)];
  });

  // selectPaths replaces the cross-location selection with the given folder paths (used by the Import
  // "Processing history" re-run); folder names are derived from the path basenames.
  function selectPaths(paths: string[]) {
    selected.value = paths.map((p) => ({
      name: baseName(p),
      path: p,
      is_dir: true,
    }));
  }

  // browseQuery builds the /api/browse query string, folding in the chosen S3 bucket/prefix when S3 is
  // active so the backend returns local/cloud/both presence for each folder. `fresh` appends the
  // cache-bypass flag the backend honours on an explicit refresh (never part of the cache key).
  function browseQuery(p?: string, fresh = false): string {
    const params = new URLSearchParams();
    if (p) params.set("path", p);
    const s3 = useS3Store();
    if (s3.active) {
      params.set("bucket", s3.bucket);
      params.set("prefix", s3.prefix);
    }
    if (fresh) params.set("fresh", "1");
    const qs = params.toString();
    return qs ? `?${qs}` : "";
  }

  // Directory-listing cache + in-flight de-duplication (keyed by the bucket/prefix-aware query, minus
  // the fresh flag). Serving the Miller-column ancestor fan-out and back-and-forth navigation from here
  // avoids re-hitting the backend; `force` bypasses the cache and re-lists live (Refresh / bucket change).
  const respCache = new Map<string, { path: string; entries: BrowseEntry[] }>();
  const inFlight = new Map<
    string,
    Promise<{ path: string; entries: BrowseEntry[] }>
  >();

  function clearCache() {
    respCache.clear();
    inFlight.clear();
  }

  async function fetchBrowse(
    p?: string,
    force = false,
  ): Promise<{ path: string; entries: BrowseEntry[] }> {
    const key = browseQuery(p);
    if (!force) {
      const cached = respCache.get(key);
      if (cached) return cached;
      const pending = inFlight.get(key);
      if (pending) return pending;
    }
    const req = apiGet<{ path: string; entries: BrowseEntry[] }>(
      `/api/browse${browseQuery(p, force)}`,
    )
      .then((data) => {
        const resp = { path: data.path, entries: data.entries || [] };
        respCache.set(key, resp);
        return resp;
      })
      .finally(() => inFlight.delete(key));
    inFlight.set(key, req);
    return req;
  }

  async function browse(p?: string, force = false) {
    loading.value = true;
    error.value = "";
    try {
      const data = await fetchBrowse(p, force);
      path.value = data.path;
      entries.value = data.entries;
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // listDir returns one directory's child folders without touching the current browse state — the
  // column (Miller) view uses it to fill the ancestor columns to the left of the active folder.
  async function listDir(p: string, force = false): Promise<BrowseEntry[]> {
    const data = await fetchBrowse(p, force);
    return data.entries;
  }

  // inspect scans one or more capture folders and merges them into a single inventory (the backend
  // unions frames/sets across all paths, so calibration in one folder satisfies lights in another).
  async function inspect(paths: string[]) {
    loading.value = true;
    error.value = "";
    inventory.value = null;
    try {
      inventory.value = await apiPost<Inventory>("/api/inspect", { paths });
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // loadPreview fetches the binary /api/preview buffer for one file. The decode is shared with the
  // live camera view (utils/previewbuf), so both read one format through one implementation.
  async function loadPreview(p: string, max?: number): Promise<PreviewImage> {
    return fetchPreviewBuffer(previewUrl(p, max));
  }

  return {
    path,
    entries,
    inventory,
    loading,
    error,
    selected,
    isSelected,
    toggleSelected,
    clearSelected,
    browse,
    listDir,
    clearCache,
    inspect,
    loadPreview,
    processedByPath,
    processedGroups,
    processingHistory,
    savedSelections,
    saveSelection,
    renameSelection,
    setSelectionFavorite,
    deleteSelection,
    loadProcessed,
    loadProcessedFor,
    selectPaths,
  };
});
