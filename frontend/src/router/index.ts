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
      path: "/calendar",
      name: "calendar",
      component: () => import("@/views/CalendarView.vue"),
    },
    // The "Processing" hub: one page, five tabs as child routes. Names are preserved from the old flat
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

export default router;
