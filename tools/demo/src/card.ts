// Title-card HTML for the intro/outro. Rendered by the same Chromium via page.setContent() and
// screenshotted to a PNG, which ffmpeg turns into a short clip (subtle zoom + fade). Themed to match the
// app's dark night-sky surface so the open/close feel native.

function escapeHtml(s: string): string {
  return s.replace(/[&<>"]/g, (c) =>
    c === "&" ? "&amp;" : c === "<" ? "&lt;" : c === ">" ? "&gt;" : "&quot;",
  );
}

export function cardHtml(opts: {
  title: string;
  subtitle?: string;
  accent?: string;
  width: number;
  height: number;
}): string {
  const accent = opts.accent || "#7c9cff";
  const title = escapeHtml(opts.title);
  const subtitle = opts.subtitle ? escapeHtml(opts.subtitle) : "";
  // A deterministic scatter of faint stars (no Math.random — keeps successive renders identical).
  const stars: string[] = [];
  let seed = 1337;
  const rnd = () => {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    return seed / 0x7fffffff;
  };
  for (let i = 0; i < 160; i++) {
    const x = (rnd() * 100).toFixed(2);
    const y = (rnd() * 100).toFixed(2);
    const r = (rnd() * 1.6 + 0.3).toFixed(2);
    const o = (rnd() * 0.6 + 0.2).toFixed(2);
    stars.push(
      `<circle cx="${x}%" cy="${y}%" r="${r}" fill="#fff" opacity="${o}"/>`,
    );
  }
  return `<!doctype html><html><head><meta charset="utf-8"><style>
    html,body{margin:0;height:100%;background:#0b0b0d;overflow:hidden}
    .wrap{position:fixed;inset:0;display:flex;flex-direction:column;align-items:center;
      justify-content:center;text-align:center;
      font-family:Inter,-apple-system,Segoe UI,system-ui,sans-serif;color:#f3f5fb}
    .glow{position:fixed;inset:0;background:
      radial-gradient(60% 50% at 50% 42%, ${accent}22, transparent 70%)}
    .logo{width:84px;height:84px;margin-bottom:28px;filter:drop-shadow(0 4px 18px ${accent}80)}
    h1{font-size:64px;font-weight:800;letter-spacing:.5px;margin:0;
      background:linear-gradient(180deg,#fff, ${accent});-webkit-background-clip:text;
      background-clip:text;color:transparent}
    p{font-size:26px;font-weight:500;color:#aeb6c6;margin:18px 0 0;max-width:70vw}
  </style></head><body>
    <svg width="100%" height="100%" style="position:fixed;inset:0">${stars.join("")}</svg>
    <div class="glow"></div>
    <div class="wrap">
      <svg class="logo" viewBox="0 0 24 24" fill="none" stroke="${accent}" stroke-width="1.6"
        stroke-linecap="round" stroke-linejoin="round">
        <path d="M12 3l2.4 5.6L20 11l-5.6 2.4L12 19l-2.4-5.6L4 11l5.6-2.4z"/>
      </svg>
      <h1>${title}</h1>
      ${subtitle ? `<p>${subtitle}</p>` : ""}
    </div>
  </body></html>`;
}
