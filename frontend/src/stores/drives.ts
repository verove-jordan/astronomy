import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost } from "@/services/api";

// External-drive browsing + "Copy to S3". Mirrors the S3 store's shape: all fetching lives here, the view
// reads state and dispatches actions. Paths are absolute host paths under the backend's browse allowlist
// (macOS /Volumes; Linux /media, /mnt, /run/media, plus ASTRO_BROWSE_ROOTS) — the backend re-validates
// every path, so a crafted path is rejected server-side regardless of the UI.

export interface DriveInfo {
  name: string;
  path: string;
  total_bytes?: number;
  free_bytes?: number;
}

export interface LocalEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size?: number;
}

// SourceInfo is one of the app's own configured directories (input/output/work) offered as a browse
// shortcut in the drive-list view — an existing, absolute path the backend widened its allow-list with.
export interface SourceInfo {
  key: "input" | "output" | "work";
  path: string;
}

interface LocalListing {
  path: string;
  parent?: string;
  entries: LocalEntry[];
}

export const useDrivesStore = defineStore("drives", () => {
  const drives = ref<DriveInfo[]>([]);
  const sources = ref<SourceInfo[]>([]); // the app's own input/output/work dirs (browse shortcuts)
  const path = ref(""); // current folder ("" = showing the drive list)
  const parent = ref(""); // parent folder for the Up action ("" = at a drive root)
  const root = ref(""); // the drive/app-folder the user entered (the column browser's left boundary)
  const entries = ref<LocalEntry[]>([]);
  const loading = ref(false);
  const error = ref("");
  // Folders checked in the column browser (full entries so the pills survive navigation).
  const selected = ref<LocalEntry[]>([]);

  // loadSources fetches the app's own dirs (input/output/work) offered alongside the drives. Best-effort:
  // a failure leaves the shortcuts empty without breaking drive browsing.
  async function loadSources(): Promise<void> {
    try {
      const data = await apiGet<{ sources: SourceInfo[] }>(
        "/api/local/sources",
      );
      sources.value = data.sources ?? [];
    } catch {
      sources.value = [];
    }
  }

  // loadDrives lists the mounted external drives and returns to the drive-list view. It also refreshes the
  // app-dir shortcuts (fire-and-forget) so they stay current whenever the drive list is shown.
  async function loadDrives(): Promise<void> {
    loading.value = true;
    error.value = "";
    void loadSources();
    try {
      const data = await apiGet<{ drives: DriveInfo[] }>("/api/local/drives");
      drives.value = data.drives ?? [];
      path.value = "";
      parent.value = "";
      root.value = "";
      entries.value = [];
      dirCache.clear(); // a fresh visit re-reads columns (drives may have been ejected/added)
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // enterRoot opens a drive/app-folder from the grid as the column browser's root.
  async function enterRoot(p: string): Promise<void> {
    root.value = p;
    await browse(p);
  }

  // browse lists one folder level under an allowed drive path.
  async function browse(p: string): Promise<void> {
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<LocalListing>(
        `/api/local/browse?path=${encodeURIComponent(p)}`,
      );
      path.value = data.path;
      parent.value = data.parent ?? "";
      entries.value = data.entries ?? [];
    } catch (e) {
      error.value = (e as Error).message;
      entries.value = [];
    } finally {
      loading.value = false;
    }
  }

  // up navigates to the parent folder, or back to the drive list at a drive root.
  async function up(): Promise<void> {
    if (parent.value) await browse(parent.value);
    else await loadDrives();
  }

  // Column fan-out support: a non-mutating directory listing with a response cache + in-flight dedup
  // (the stores/browse.ts pattern), so the Miller columns don't re-hit the backend on every hop.
  const dirCache = new Map<string, LocalEntry[]>();
  const dirInflight = new Map<string, Promise<LocalEntry[]>>();
  async function listDir(p: string): Promise<LocalEntry[]> {
    const hit = dirCache.get(p);
    if (hit) return hit;
    const running = dirInflight.get(p);
    if (running) return running;
    const req = (async () => {
      try {
        const data = await apiGet<LocalListing>(
          `/api/local/browse?path=${encodeURIComponent(p)}`,
        );
        const list = data.entries ?? [];
        dirCache.set(p, list);
        return list;
      } finally {
        dirInflight.delete(p);
      }
    })();
    dirInflight.set(p, req);
    return req;
  }

  // Selection over the column browser (folders only — the upload lane copies folders).
  function isSelected(p: string): boolean {
    return selected.value.some((e) => e.path === p);
  }
  function toggleSelected(entry: LocalEntry): void {
    if (isSelected(entry.path)) {
      selected.value = selected.value.filter((e) => e.path !== entry.path);
    } else {
      selected.value = [...selected.value, entry];
    }
  }
  function clearSelected(): void {
    selected.value = [];
  }

  // copyToS3 enqueues a smart, content-verified copy (upload only missing/corrupted files) of an
  // external-drive folder to S3, mirrored under <prefix>/<folderName>/. Returns the new job id — the caller
  // navigates to the job page, where the shared job SSE stream drives the live progress bar.
  async function copyToS3(
    sourcePath: string,
    bucket: string,
    prefix: string,
  ): Promise<number> {
    const data = await apiPost<{ id: number }>("/api/local/upload", {
      path: sourcePath,
      bucket,
      prefix,
    });
    return data.id;
  }

  return {
    drives,
    sources,
    path,
    parent,
    root,
    entries,
    loading,
    error,
    selected,
    loadDrives,
    loadSources,
    browse,
    enterRoot,
    up,
    listDir,
    isSelected,
    toggleSelected,
    clearSelected,
    copyToS3,
  };
});
