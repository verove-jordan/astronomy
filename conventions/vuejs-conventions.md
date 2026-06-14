# Vue.js Conventions

For Vue 3 SPAs. Reference stack: Vue 3.5 + TypeScript + Pinia + Tailwind + Vite
(+ a component library such as Ant Design Vue). Test rules in
`testing-conventions.md`; styling in `tailwind-conventions.md`.

## Pinia Stores

- **All API calls live in store actions — never in components.** Components dispatch actions and read state.
- **Caching**: skip refetch if data is already present and `force === false`.
- **Deduplication**: keep an in-flight request map so concurrent callers share one request.
- **Pagination**: track offset/cursor + `hasMore` in the store.
- **Cross-store hydration**: have stores hydrate their dependencies (auth → company → reference data) rather than components orchestrating fetch order.
- Persist only what must survive reload (`pinia-plugin-persistedstate`), e.g. session/login.
- Pick one store style (Options or Setup) per project and stay consistent.

## API Layer

- Centralize fetching in one wrapper (`safeFetch` / `safeFetchJson`) that maps failures to app state: `401` → clear auth + redirect, `403` → forbidden state, `5xx` → server-error state.
- Use `AbortController` for cancellable requests (searches, type-ahead).
- Build the base URL from `import.meta.env` vars; don't hardcode hosts.

## Components

### SVG Icons — one file each

**Never inline SVG icon markup** in feature components or pages. Every icon lives in its own `Icon<Name>.vue` under a shared `Icons/` directory so it can be reused, re-themed, and swapped centrally. Use the canonical color-prop pattern:

```vue
<script setup lang="ts">
const _props = defineProps<{ color?: string }>();
</script>
<template>
  <svg :stroke="color" :fill="color">...</svg>
</template>
```

The consumer passes a resolved color; the icon never hardcodes stroke/fill. The `_props` name is intentional — props exist for the template binding, not to be read in script.

### SVG images & logos

Render at their **intrinsic aspect ratio**. Never force a non-square logo into a square frame or stretch it. Use `object-contain` / `max-width` + `height:auto` so wide or tall artwork stays legible.

### Tables

Every user-facing table must, without exception:

1. **Chained sort on headers** — click to sort; shift-click chains keys; the header reflects direction + order index.
2. **Text search on searchable columns** — free-text filtering of visible rows.

Compose these from one shared `GenericTable` + shared filter primitives. Do not reimplement sort/search per page.

### Shared primitives

Build shared `BaseModal`, `Card`, `PaginationBar`, dropdown/selector, etc. once and reuse. Don't re-style or re-invent a primitive locally — extend the shared one (add a prop / compact mode).

## Composables

Extract reusable stateful logic into `useX` composables rather than duplicating across components — URL ↔ state sync, theme colors, toasts, undo/redo, clipboard, a shared IntersectionObserver, etc. A polymorphic widget (e.g. a tree-select) should be composed from several small composables, not one mega-component.

## i18n

All user-facing text goes through `t('key')`. New keys must be added to **every** locale file (`en`, `fr`, …) — never leave a locale missing a key.

## Routing & Permissions

- Guard routes in `router.beforeEach`; check a permission/page map for authenticated users and fall back to a full auth check when needed.
- Keep route → permission mapping in one place.

## Type Safety & Compile Checks

When validating your own edits, use lightweight compile-safety only: `pnpm lint` and `pnpm vue-tsc --noEmit`. Reserve full `pnpm test` / `pnpm build` for CI and manual runs.
