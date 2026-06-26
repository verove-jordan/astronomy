import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet } from "@/services/api";
import type { Master } from "@/types";

export const useLibraryStore = defineStore("library", () => {
  const masters = ref<Master[]>([]);
  const loading = ref(false);
  const error = ref("");

  async function load() {
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ masters: Master[] }>("/api/masters");
      masters.value = data.masters || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  return { masters, loading, error, load };
});
