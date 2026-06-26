/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{vue,ts}'],
  theme: {
    extend: {
      colors: {
        // Brand scale (deep-space blues): surfaces → text.
        brand: {
          50: '#eef2ff',
          100: '#e0e7ff',
          200: '#c7d2fe',
          300: '#a5b4fc',
          400: '#818cf8',
          500: '#6366f1',
          600: '#4f46e5',
          700: '#4338ca',
          800: '#3730a3',
          900: '#312e81',
        },
        // Semantic scales — keep DEFAULT so existing text-success/text-warning/text-danger still resolve.
        success: { DEFAULT: '#16a34a', 50: '#f0fdf4', 100: '#dcfce7', 300: '#86efac', 500: '#22c55e', 600: '#16a34a', 700: '#15803d' },
        warning: { DEFAULT: '#d97706', 50: '#fffbeb', 100: '#fef3c7', 300: '#fcd34d', 500: '#f59e0b', 600: '#d97706', 700: '#b45309' },
        danger: { DEFAULT: '#dc2626', 50: '#fef2f2', 100: '#fee2e2', 300: '#fca5a5', 500: '#ef4444', 600: '#dc2626', 700: '#b91c1c' },
        info: { DEFAULT: '#0284c7', 50: '#f0f9ff', 100: '#e0f2fe', 300: '#7dd3fc', 500: '#0ea5e9', 600: '#0284c7', 700: '#0369a1' },
        // Per-filter domain colors (mirrored to JS in constants/colors.ts for charts/canvas).
        filter: {
          l: '#cbd5e1',
          r: '#ef4444',
          g: '#22c55e',
          b: '#3b82f6',
          ha: '#f43f5e',
          'l-soft': '#e2e8f0',
          'r-soft': '#fca5a5',
          'g-soft': '#86efac',
          'b-soft': '#93c5fd',
          'ha-soft': '#fda4af',
        },
        // Dark-mode elevation surfaces (slightly blue-shifted slate) for a comfortable, layered UI.
        // NOTE: names must not prefix-collide (e.g. `dark` vs `dark-raised`) or Tailwind cannot
        // disambiguate `bg-surface-dark-raised` and silently drops it — keep them distinct words.
        surface: {
          DEFAULT: '#ffffff',
          muted: '#f8fafc',
          // Dark-mode night sky: very dark, near-neutral grey-black (minimal blue), deep → elevated.
          deep: '#0b0b0d',
          raised: '#161619',
          elevated: '#1f1f23',
        },
      },
    },
  },
  plugins: [],
}
