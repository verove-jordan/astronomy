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

interface LocalListing {
  path: string;
  parent?: string;
  entries: LocalEntry[];
}

export const useDrivesStore = defineStore("drives", () => {
  const drives = ref<DriveInfo[]>([]);
  const path = ref(""); // current folder ("" = showing the drive list)
  const parent = ref(""); // parent folder for the Up action ("" = at a drive root)
  const entries = ref<LocalEntry[]>([]);
  const loading = ref(false);
  const error = ref("");

  // loadDrives lists the mounted external drives and returns to the drive-list view.
  async function loadDrives(): Promise<void> {
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ drives: DriveInfo[] }>("/api/local/drives");
      drives.value = data.drives ?? [];
      path.value = "";
      parent.value = "";
      entries.value = [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
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
    path,
    parent,
    entries,
    loading,
    error,
    loadDrives,
    browse,
    up,
    copyToS3,
  };
});
