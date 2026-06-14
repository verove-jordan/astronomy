# Tailwind & Design-System Conventions

Reusable methodology for a Tailwind-based design system. The brand palette is
per-project — fill the stub below; the rules are universal.

## Theme is the single source of truth

- Brand colors live in `tailwind.config.js > theme.extend.colors`. If JS needs the same values, re-export them from one `constants/colors.ts` and keep them in sync (config is authoritative).
- **No hardcoded hex in templates** — use utility classes. Inline styles only for genuinely dynamic values (e.g. a server-provided color).
- New colors flow one way: add to the Tailwind config first, then mirror to JS only if needed.

### Brand palette stub (define per project)

| Token | Hex | Role |
|---|---|---|
| `brand-50 … brand-900` | _TBD_ | Primary scale (surfaces → text) |
| `success` / `warning` / `danger` | _TBD_ | Semantic states |

## Dark mode

- Mechanism: `darkMode: 'class'`; toggle `dark` on `<html>`, persist the choice in `localStorage`.
- **Every color / background / border utility in a template must have a `dark:` counterpart.**
- Icons use `currentColor` for automatic theme adaptation — never hardcode a fill.
- Dynamic hex colors (charts, user-chosen colors) pass through a single `adjustColorForTheme()` helper so they stay legible in both themes.

## The JIT rule (most common bug)

**Never assemble class names by string concatenation.** Tailwind's JIT compiler only emits classes it finds as complete literals in source; a built-up name like `` `bg-${c}-100` `` is purged from the production bundle. Style maps keyed by a runtime value MUST store complete static strings:

```ts
const tint = { chart: 'bg-blue-100/60 dark:bg-blue-900/30',
               gallery: 'bg-violet-100/60 dark:bg-violet-900/30' }[type] ?? 'bg-gray-100'
```

## CSS layer boundaries

- `@apply` is allowed **only** in global CSS files for third-party overrides — not in component styles. Components use utilities directly in the template.
- Reference tokens in CSS via the `theme()` function; never hardcode hex in CSS.
- `!important` is allowed **only** to override a third-party component library — never for your own components.

## Shared, not re-invented

- **One** loading indicator component for every non-button surface (tables, charts, modals, page loaders). Buttons keep their own inline spinner. The loader must be accessible (`role="status"` + a visually-hidden label) and honor `prefers-reduced-motion`.
- **One** pill/chip component and **one** hover-tooltip component, reused everywhere. Add a `compact`/variant prop rather than re-styling locally.
- Collect repeated class combinations (button, selector, focus-ring) into a `constants/styles.ts` once a pattern appears in 2+ components.

## Motion & accessibility

- Gate every reveal/slide/entrance animation behind `motion-safe:` / `prefers-reduced-motion`.
- Decorative overlays are `aria-hidden="true"`; status elements use `role="status"`.

## Don'ts

- No hardcoded hex in templates or CSS. No dynamically-concatenated class names. No `@apply` outside global CSS. No `!important` on your own components. No duplicate source of truth for color values beyond the config + one JS mirror.
