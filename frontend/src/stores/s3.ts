import { defineStore } from "pinia";
import { ref, computed } from "vue";
import {
  apiGet,
  apiPost,
  S3_BUCKET_KEY as BUCKET_KEY,
  S3_PREFIX_KEY as PREFIX_KEY,
} from "@/services/api";
import type { S3Status, BrowseEntry } from "@/types";

// S3 connection + the user's chosen bucket/prefix (persisted locally; credentials stay in the backend
// env). Drives the import browser's presence badges and the per-folder transfer actions. The storage keys
// are owned by services/api.ts so the URL builders there can tag previews with the active bucket/prefix.

export type TransferOp = "upload" | "sync" | "download" | "removeLocal";

export const useS3Store = defineStore("s3", () => {
  const status = ref<S3Status | null>(null);
  const bucket = ref(localStorage.getItem(BUCKET_KEY) || "");
  const prefix = ref(localStorage.getItem(PREFIX_KEY) || "");
  const loading = ref(false);
  const error = ref("");

  // Real-bucket browse for the Import "S3 Storage" tab: s3Rel is the current sub-path (relative to the
  // configured prefix), s3Entries the folders/files there, s3Selected the checked S3 folders (their `path`
  // is the rel, used to download to <DataDir>/<rel>). Independent of the local browse + the data/ mirror.
  const s3Rel = ref("");
  const s3Entries = ref<BrowseEntry[]>([]);
  const s3Selected = ref<BrowseEntry[]>([]);

  const configured = computed(() => status.value?.configured ?? false);
  const reachable = computed(() => status.value?.reachable ?? false);
  const buckets = computed(() => status.value?.buckets ?? []);
  // S3 features are usable once credentials are configured AND a bucket is chosen.
  const active = computed(() => configured.value && bucket.value !== "");

  async function fetchStatus(): Promise<void> {
    loading.value = true;
    try {
      status.value = await apiGet<S3Status>("/api/s3/status");
      // Default the bucket to the only one if none is chosen yet.
      if (!bucket.value && (status.value.buckets?.length ?? 0) === 1) {
        setBucket(status.value.buckets![0]);
      }
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  function setBucket(b: string) {
    bucket.value = b;
    localStorage.setItem(BUCKET_KEY, b);
  }
  function setPrefix(p: string) {
    prefix.value = p.replace(/^\/+|\/+$/g, ""); // no leading/trailing slashes
    localStorage.setItem(PREFIX_KEY, prefix.value);
  }

  // transfer enqueues an upload/sync/download/removeLocal job for one folder (relative to the data or
  // output root) and returns the new job id — it then streams progress through the job SSE stack.
  async function transfer(
    op: TransferOp,
    relPath: string,
    namespace: "data" | "output" = "data",
  ): Promise<number> {
    const data = await apiPost<{ id: number }>("/api/s3/transfer", {
      op,
      namespace,
      rel_path: relPath,
      bucket: bucket.value,
      prefix: prefix.value,
    });
    return data.id;
  }

  // s3Query builds the /api/s3/browse query for the current bucket/prefix at a sub-path.
  function s3Query(rel: string): string {
    return new URLSearchParams({
      bucket: bucket.value,
      prefix: prefix.value,
      rel,
    }).toString();
  }

  // s3Browse lists the real bucket at <prefix>/<rel> (default connection) into s3Rel/s3Entries.
  async function s3Browse(rel: string): Promise<void> {
    loading.value = true;
    try {
      const data = await apiGet<{ rel: string; entries: BrowseEntry[] }>(
        `/api/s3/browse?${s3Query(rel)}`,
      );
      s3Rel.value = data.rel ?? rel;
      s3Entries.value = data.entries ?? [];
    } catch (e) {
      error.value = (e as Error).message;
      s3Entries.value = [];
    } finally {
      loading.value = false;
    }
  }

  // s3ListDir fetches one S3 sub-path's entries without touching current state (FileBrowser ancestors).
  async function s3ListDir(rel: string): Promise<BrowseEntry[]> {
    const data = await apiGet<{ entries: BrowseEntry[] }>(
      `/api/s3/browse?${s3Query(rel)}`,
    );
    return data.entries ?? [];
  }

  function toggleS3(entry: BrowseEntry): void {
    const i = s3Selected.value.findIndex((s) => s.path === entry.path);
    if (i >= 0) s3Selected.value.splice(i, 1);
    else s3Selected.value.push(entry);
  }
  function clearS3(): void {
    s3Selected.value = [];
  }

  // importFolders downloads each selected real-bucket folder (<prefix>/<rel>) to <DataDir>/<rel> and
  // resolves once every download finishes, so the caller can inspect/run them as local captures. Throws
  // if any fails. Byte progress streams to the Tasks list.
  async function importFolders(rels: string[]): Promise<void> {
    const ids = await Promise.all(
      rels.map((rel) =>
        apiPost<{ id: number }>("/api/s3/import", {
          bucket: bucket.value,
          prefix: prefix.value,
          rel,
        }).then((d) => d.id),
      ),
    );
    await Promise.all(ids.map(waitForJob));
  }

  // downloadFolders pulls each S3 capture folder (by data-relative path) to local and resolves only once
  // every download finishes — so the caller can then inspect/run over local files. Throws if any fails.
  // Byte progress streams to the Tasks list via the transfer jobs.
  async function downloadFolders(rels: string[]): Promise<void> {
    const ids = await Promise.all(rels.map((rel) => transfer("download", rel)));
    await Promise.all(ids.map(waitForJob));
  }

  async function waitForJob(id: number): Promise<void> {
    const terminal = ["succeeded", "failed", "cancelled"];
    for (;;) {
      const j = await apiGet<{ status: string; error?: string }>(
        `/api/jobs/${id}`,
      );
      if (terminal.includes(j.status)) {
        if (j.status !== "succeeded")
          throw new Error(j.error || `download job ${id} ${j.status}`);
        return;
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
  }

  return {
    status,
    bucket,
    prefix,
    loading,
    error,
    configured,
    reachable,
    buckets,
    active,
    fetchStatus,
    setBucket,
    setPrefix,
    transfer,
    downloadFolders,
    s3Rel,
    s3Entries,
    s3Selected,
    s3Browse,
    s3ListDir,
    toggleS3,
    clearS3,
    importFolders,
  };
});
