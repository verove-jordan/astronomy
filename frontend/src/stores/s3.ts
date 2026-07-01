import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { S3Status } from "@/types";

// S3 connection + the user's chosen bucket/prefix (persisted locally; credentials stay in the backend
// env). Drives the import browser's presence badges and the per-folder transfer actions.
const BUCKET_KEY = "astrostack.s3.bucket";
const PREFIX_KEY = "astrostack.s3.prefix";

export type TransferOp = "upload" | "sync" | "download" | "removeLocal";

export const useS3Store = defineStore("s3", () => {
  const status = ref<S3Status | null>(null);
  const bucket = ref(localStorage.getItem(BUCKET_KEY) || "");
  const prefix = ref(localStorage.getItem(PREFIX_KEY) || "");
  const loading = ref(false);
  const error = ref("");

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
  };
});
