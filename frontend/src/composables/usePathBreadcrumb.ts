import { computed, toValue, type MaybeRefOrGetter } from "vue";

export interface Crumb {
  label: string;
  path: string;
}

// usePathBreadcrumb splits a path into clickable segments, clamped to the root so navigation can't
// climb above it (the root is shown as the first crumb). Two root conventions are supported:
//   - a non-empty absolute root (the local file browser): crumbs are absolute paths under it.
//   - an EMPTY root (the S3 browser, rooted at the bucket): the bucket root is a first-class crumb
//     with path "" so it stays as the leftmost Miller column while you descend (its children are the
//     bucket top-level), and rel crumbs accumulate WITHOUT a leading slash so their paths equal the
//     rels the caller navigates with (e.g. "M51", "M51/autorun") — matching the backend's rel model.
//   `rootLabel` names the empty-root crumb (default "/").
export function usePathBreadcrumb(
  path: MaybeRefOrGetter<string>,
  root: MaybeRefOrGetter<string>,
  rootLabel: MaybeRefOrGetter<string> = "/",
) {
  return computed<Crumb[]>(() => {
    const p = (toValue(path) || "").replace(/\/+$/, "");
    const r = (toValue(root) || "").replace(/\/+$/, "");
    const emptyRoot = r === "";

    // Empty-root (S3): the bucket root is always the first crumb; rel segments accumulate plainly.
    if (emptyRoot) {
      const crumbs: Crumb[] = [{ label: toValue(rootLabel) || "/", path: "" }];
      let acc = "";
      for (const seg of p ? p.split("/") : []) {
        if (!seg) continue;
        acc = acc ? acc + "/" + seg : seg;
        crumbs.push({ label: seg, path: acc });
      }
      return crumbs;
    }

    if (!p) return [];
    let base = "";
    let rel = p;
    if (p === r || p.startsWith(r + "/")) {
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
