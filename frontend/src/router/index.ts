import { createRouter, createWebHistory } from "vue-router";

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: "/", redirect: { name: "import" } },
    {
      path: "/tonight",
      name: "tonight",
      component: () => import("@/views/TonightView.vue"),
    },
    {
      path: "/goto",
      name: "goto",
      component: () => import("@/views/GotoView.vue"),
    },
    {
      path: "/capture",
      name: "capture",
      component: () => import("@/views/CaptureView.vue"),
    },
    {
      path: "/logbook",
      name: "logbook",
      component: () => import("@/views/LogbookView.vue"),
    },
    {
      path: "/logbook/:id",
      name: "logbookSession",
      component: () => import("@/views/LogbookSessionView.vue"),
      props: true,
    },
    {
      path: "/mosaic",
      name: "mosaic",
      component: () => import("@/views/MosaicView.vue"),
    },
    {
      path: "/calendar",
      name: "calendar",
      component: () => import("@/views/CalendarView.vue"),
    },
    {
      path: "/solarsystem",
      name: "solarsystem",
      component: () => import("@/views/SolarSystemView.vue"),
    },
    {
      path: "/astroagent",
      name: "astroagent",
      component: () => import("@/views/AstroAgentView.vue"),
    },
    // The "Processing" hub: one page, six tabs as child routes. Names are preserved from the old flat
    // routes so existing `router.push({ name: "job" })` calls keep working.
    {
      path: "/processing",
      component: () => import("@/views/ProcessingView.vue"),
      children: [
        { path: "", redirect: { name: "import" } },
        {
          path: "import",
          name: "import",
          component: () => import("@/views/ImportView.vue"),
        },
        {
          path: "live",
          name: "livestack",
          component: () => import("@/views/LiveStackView.vue"),
        },
        {
          path: "tasks",
          name: "jobs",
          component: () => import("@/views/JobsListView.vue"),
        },
        {
          path: "tasks/:id",
          name: "job",
          component: () => import("@/views/JobView.vue"),
          props: true,
        },
        {
          path: "runs",
          name: "runs",
          component: () => import("@/views/RunsView.vue"),
        },
        {
          path: "library",
          name: "library",
          component: () => import("@/views/LibraryView.vue"),
        },
        {
          path: "storage",
          name: "storage",
          component: () => import("@/views/StorageView.vue"),
        },
        // Drives merged into Storage — keep the old path working (bookmarks / in-app links).
        { path: "drives", redirect: { name: "storage" } },
      ],
    },

    // Back-compat: the old flat paths redirect to the nested routes (bookmarks / external links).
    { path: "/import", redirect: { name: "import" } },
    { path: "/livestack", redirect: { name: "livestack" } },
    { path: "/jobs", redirect: { name: "jobs" } },
    {
      path: "/jobs/:id",
      redirect: (to) => ({ name: "job", params: to.params }),
    },
    { path: "/runs", redirect: { name: "runs" } },
    { path: "/library", redirect: { name: "library" } },
  ],
});

// Recover from a failed lazy route-chunk import. After the frontend is redeployed, an already-open tab
// still references the previous build's content-hashed chunk names; those requests 404 (nginx serves
// index.html for them — hence the "text/html" MIME error) and the dynamic import rejects, which would
// otherwise leave navigation permanently stuck. A single full reload fetches the fresh index.html + its
// current chunks. Guarded (once per 10s via sessionStorage) so a genuinely persistent failure can't loop.
function isChunkLoadError(err: unknown): boolean {
  const msg = (err as { message?: string })?.message ?? "";
  return (
    /dynamically imported module/i.test(msg) || // Chrome/Firefox: "Failed to fetch dynamically imported module"
    /Importing a module script failed/i.test(msg) || // Safari
    /Unable to preload CSS/i.test(msg)
  );
}

const RELOAD_FLAG = "astrostack.chunkReloadAt";
function reloadForFreshChunks() {
  try {
    const last = Number(sessionStorage.getItem(RELOAD_FLAG) || 0);
    if (Date.now() - last < 10_000) return; // already reloaded moments ago — don't loop
    sessionStorage.setItem(RELOAD_FLAG, String(Date.now()));
  } catch {
    // sessionStorage unavailable (private mode quota) — fall through to a single reload attempt
  }
  window.location.reload();
}

// A lazy route component that fails to load surfaces here as a navigation error.
router.onError((err) => {
  if (isChunkLoadError(err)) reloadForFreshChunks();
});

// Vite fires this when a <link rel="modulepreload"> for a route chunk fails (before the import runs).
window.addEventListener("vite:preloadError", (e) => {
  e.preventDefault();
  reloadForFreshChunks();
});

export default router;
