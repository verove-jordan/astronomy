import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { apiGet, apiPost, previewUrl, withS3 } from "@/services/api";
import { PROCESSED_GROUP_COLORS } from "@/constants/colors";
import { baseName } from "@/utils/format";
import { useS3Store } from "@/stores/s3";
import type {
  BrowseEntry,
  Inventory,
  PreviewImage,
  ProcessedFolder,
  ProcessedGroup,
  ProcessingHistoryEntry,
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

  async function loadProcessed() {
    try {
      const data = await apiGet<{ groups: ProcessedGroup[] }>(
        withS3("/api/processed"),
      );
      processedGroups.value = data.groups ?? [];
    } catch {
      processedGroups.value = [];
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

  // processingHistory de-duplicates past jobs by their folder-set so the Import view can offer each
  // unique selection for re-running. Most-recent first; runs counts how many jobs used that exact set.
  const processingHistory = computed<ProcessingHistoryEntry[]>(() => {
    const recent = [...processedGroups.value].sort(
      (a, b) => (b.created_at_ms || 0) - (a.created_at_ms || 0),
    );
    const bySig = new Map<string, ProcessingHistoryEntry>();
    for (const g of recent) {
      const paths = g.paths ?? [];
      if (!paths.length) continue;
      const sig = paths
        .map((p) => p.path.toLowerCase())
        .sort()
        .join("|");
      const existing = bySig.get(sig);
      if (existing) {
        existing.runs++;
        continue;
      }
      bySig.set(sig, {
        jobId: g.job_id,
        object: g.object,
        mode: g.mode,
        format: g.format,
        status: g.status,
        createdAtMs: g.created_at_ms,
        runs: 1,
        paths,
      });
    }
    return [...bySig.values()];
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
  // active so the backend returns local/cloud/both presence for each folder.
  function browseQuery(p?: string): string {
    const params = new URLSearchParams();
    if (p) params.set("path", p);
    const s3 = useS3Store();
    if (s3.active) {
      params.set("bucket", s3.bucket);
      params.set("prefix", s3.prefix);
    }
    const qs = params.toString();
    return qs ? `?${qs}` : "";
  }

  async function browse(p?: string) {
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ path: string; entries: BrowseEntry[] }>(
        `/api/browse${browseQuery(p)}`,
      );
      path.value = data.path;
      entries.value = data.entries || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // listDir returns one directory's child folders without touching the current browse state — the
  // column (Miller) view uses it to fill the ancestor columns to the left of the active folder.
  async function listDir(p: string): Promise<BrowseEntry[]> {
    const data = await apiGet<{ path: string; entries: BrowseEntry[] }>(
      `/api/browse${browseQuery(p)}`,
    );
    return data.entries || [];
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

  // loadPreview fetches the binary /api/preview buffer for one file and parses its header into a
  // PreviewImage (little-endian, as all browser-host platforms are). The viewer stretches it locally.
  async function loadPreview(p: string, max?: number): Promise<PreviewImage> {
    const res = await fetch(previewUrl(p, max));
    if (!res.ok) {
      let message = res.statusText;
      try {
        const data = (await res.json()) as { error?: string };
        if (data.error) message = data.error;
      } catch {
        // keep statusText
      }
      throw new Error(message);
    }
    const buf = await res.arrayBuffer();
    const head = new DataView(buf);
    const w = head.getUint32(0, true);
    const h = head.getUint32(4, true);
    const c = head.getUint32(8, true);
    const autoLo = head.getUint16(12, true);
    const autoHi = head.getUint16(14, true);
    const data = new Uint16Array(buf, 16, w * h * c);
    return { w, h, c, autoLo, autoHi, data };
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
    inspect,
    loadPreview,
    processedByPath,
    processingHistory,
    loadProcessed,
    selectPaths,
  };
});
