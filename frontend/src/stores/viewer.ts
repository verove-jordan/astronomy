import { defineStore } from "pinia";
import { ref } from "vue";

// useViewerStore is the app-wide single FileViewer modal: any component (file tables, the inspector,
// the frame review) calls open(path) to view a capture file full-screen, and one <FileViewer> mounted
// in App.vue renders it. Centralizing it avoids a viewer instance per table.
export const useViewerStore = defineStore("viewer", () => {
  const path = ref<string | null>(null);

  function open(p: string) {
    path.value = p;
  }
  function close() {
    path.value = null;
  }

  return { path, open, close };
});
