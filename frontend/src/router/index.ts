import { createRouter, createWebHistory } from "vue-router";

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: "/", redirect: "/import" },
    {
      path: "/import",
      name: "import",
      component: () => import("@/views/ImportView.vue"),
    },
    {
      path: "/jobs",
      name: "jobs",
      component: () => import("@/views/JobsListView.vue"),
    },
    {
      path: "/jobs/:id",
      name: "job",
      component: () => import("@/views/JobView.vue"),
      props: true,
    },
    {
      path: "/runs",
      name: "runs",
      component: () => import("@/views/RunsView.vue"),
    },
    {
      path: "/library",
      name: "library",
      component: () => import("@/views/LibraryView.vue"),
    },
  ],
});

export default router;
