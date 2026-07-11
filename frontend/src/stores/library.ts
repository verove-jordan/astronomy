import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost } from "@/services/api";
import type { Master, PhoneMaster } from "@/types";

export const useLibraryStore = defineStore("library", () => {
  const masters = ref<Master[]>([]);
  const phoneMasters = ref<PhoneMaster[]>([]);
  const loading = ref(false);
  const error = ref("");
  // Copy-library-to-S3 state (mirrors the whole master library to <prefix>/library/ as a background job).
  const copying = ref(false);
  const copyError = ref("");
  const copiedJobId = ref<number | null>(null);

  async function load() {
    loading.value = true;
    error.value = "";
    try {
      const [deep, phone] = await Promise.all([
        apiGet<{ masters: Master[] }>("/api/masters"),
        apiGet<{ masters: PhoneMaster[] }>("/api/phone-masters"),
      ]);
      masters.value = deep.masters || [];
      phoneMasters.value = phone.masters || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // copyToS3 mirrors the calibration-master library to <prefix>/library/ on S3 (a verified per-file sync,
  // keeping the local copies). It returns the enqueued job id so the UI can link to the live Tasks view.
  async function copyToS3(
    bucket: string,
    prefix: string,
  ): Promise<number | null> {
    copying.value = true;
    copyError.value = "";
    copiedJobId.value = null;
    try {
      const { id } = await apiPost<{ id: number }>("/api/library/s3-sync", {
        bucket,
        prefix,
      });
      copiedJobId.value = id;
      return id;
    } catch (e) {
      copyError.value = (e as Error).message;
      return null;
    } finally {
      copying.value = false;
    }
  }

  return {
    masters,
    phoneMasters,
    loading,
    error,
    copying,
    copyError,
    copiedJobId,
    load,
    copyToS3,
  };
});
