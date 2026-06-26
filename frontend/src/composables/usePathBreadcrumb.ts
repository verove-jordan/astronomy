import { computed, toValue, type MaybeRefOrGetter } from "vue";

export interface Crumb {
  label: string;
  path: string;
}

// usePathBreadcrumb splits an absolute path into clickable segments, clamped to the data root so
// navigation can't climb above it (the root is shown as the first crumb).
export function usePathBreadcrumb(
  path: MaybeRefOrGetter<string>,
  root: MaybeRefOrGetter<string>,
) {
  return computed<Crumb[]>(() => {
    const p = (toValue(path) || "").replace(/\/+$/, "");
    const r = (toValue(root) || "").replace(/\/+$/, "");
    if (!p) return [];

    let base = "";
    let rel = p;
    if (r && (p === r || p.startsWith(r + "/"))) {
      base = r;
      rel = p.slice(r.length).replace(/^\/+/, "");
    }

    const crumbs: Crumb[] = [];
    if (base) crumbs.push({ label: base.split("/").pop() || base, path: base });
    let acc = base;
    for (const seg of rel ? rel.split("/") : []) {
      if (!seg) continue;
      acc = acc ? acc + "/" + seg : "/" + seg;
      crumbs.push({ label: seg, path: acc });
    }
    if (crumbs.length === 0)
      crumbs.push({ label: p.split("/").pop() || p, path: p });
    return crumbs;
  });
}
