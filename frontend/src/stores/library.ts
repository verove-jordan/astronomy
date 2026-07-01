import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet } from "@/services/api";
import type { Master, PhoneMaster } from "@/types";

export const useLibraryStore = defineStore("library", () => {
  const masters = ref<Master[]>([]);
  const phoneMasters = ref<PhoneMaster[]>([]);
  const loading = ref(false);
  const error = ref("");

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

  return { masters, phoneMasters, loading, error, load };
});
