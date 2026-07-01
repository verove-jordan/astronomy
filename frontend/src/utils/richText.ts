import MarkdownIt from "markdown-it";

// Renders a model reply (Markdown, possibly with fenced code / JSON) into safe HTML for v-html.
// Local vision models answer in Markdown and sometimes in raw JSON; this turns both into styled
// output instead of the literal "###"/"{" the terminal-style transport delivers.

// escapeHtml neutralizes the five HTML-significant characters so model text can never inject markup.
export function escapeHtml(s: string): string {
  return s.replace(
    /[&<>"']/g,
    (c) =>
      ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      })[c] as string,
  );
}

// jsonValueToHtml pretty-prints a parsed JSON value into indented, syntax-highlighted HTML. Each
// primitive is re-serialized with JSON.stringify (correct string/number/escape rules) then
// HTML-escaped, so the output is both valid JSON to read and safe to inject.
function jsonValueToHtml(value: unknown, indent: number): string {
  const pad = "  ".repeat(indent);
  const padIn = "  ".repeat(indent + 1);

  if (value === null) return `<span class="tok-null">null</span>`;
  const t = typeof value;
  if (t === "number" || t === "boolean")
    return `<span class="tok-${t === "number" ? "num" : "bool"}">${escapeHtml(
      JSON.stringify(value),
    )}</span>`;
  if (t === "string")
    return `<span class="tok-str">${escapeHtml(JSON.stringify(value))}</span>`;

  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    const items = value
      .map((v) => padIn + jsonValueToHtml(v, indent + 1))
      .join(",\n");
    return `[\n${items}\n${pad}]`;
  }
  if (t === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    if (entries.length === 0) return "{}";
    const items = entries
      .map(
        ([k, v]) =>
          `${padIn}<span class="tok-key">${escapeHtml(
            JSON.stringify(k),
          )}</span>: ${jsonValueToHtml(v, indent + 1)}`,
      )
      .join(",\n");
    return `{\n${items}\n${pad}}`;
  }
  return escapeHtml(String(value)); // undefined/function/symbol never survive JSON, but stay safe
}

// formatJsonBlock renders a JSON string as a highlighted <pre> block, or null when it isn't valid JSON
// (the caller then falls back to plain code/Markdown). Starting with "<pre" lets markdown-it skip its
// own wrapper when this is returned from its `highlight` hook.
export function formatJsonBlock(raw: string): string | null {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  return `<pre class="astro-code astro-json"><code>${jsonValueToHtml(value, 0)}</code></pre>`;
}

// isJsonlike is a cheap gate before JSON.parse: only object/array literals are auto-treated as JSON,
// so a bare code block containing `42` or `"hi"` (valid JSON, but plainly prose) stays literal.
function isJsonlike(s: string): boolean {
  return /^[[{]/.test(s.trim());
}

// html:false → model output can never inject raw HTML/scripts; markdown-it also rejects javascript:
// links by default. linkify makes bare URLs clickable; breaks turns single newlines into <br>. The
// highlight hook pretty-prints ```json fences (and bare-JSON, no-language fences) via formatJsonBlock;
// returning "" falls back to markdown-it's default escaped <pre><code> for every other language.
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
  highlight: (str, lang) => {
    const info = (lang || "").trim().toLowerCase();
    const isJson = info === "json" || info === "jsonc" || info === "json5";
    if (isJson || (!info && isJsonlike(str))) return formatJsonBlock(str) ?? "";
    return "";
  },
});

// renderModelText is the single entry point: a reply that is itself one JSON value renders as a
// highlighted JSON block; anything else renders as Markdown (with JSON fences highlighted inline).
export function renderModelText(text: string): string {
  const src = text || "";
  if (isJsonlike(src)) {
    const json = formatJsonBlock(src.trim());
    if (json) return json;
  }
  return md.render(src);
}
