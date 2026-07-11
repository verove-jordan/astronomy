// The in-page "demo runtime". Injected via context.addInitScript so it runs at document-start on every
// navigation, re-creating its overlay layer (the DOM is wiped on each page load). It renders directly
// into the page, so everything here is captured natively in Playwright's video — no compositing.
//
// It exposes window.__demo, which the driver (actions.ts) calls via page.evaluate:
//   moveCursor(x,y,ms) → Promise   ripple()   caption(text)   clearCaption()   spotlight(rect|null)
//
// Keep this a single self-contained function: Playwright serialises it with toString() and the config
// arg, so it must not reference any module-scope binding.

export interface RuntimeConfig {
  accent: string; // ripple / caption accent colour
}

export interface DemoApi {
  moveCursor(x: number, y: number, ms: number): Promise<void>;
  ripple(): void;
  caption(text: string): void;
  clearCaption(): void;
  spotlight(rect: { x: number; y: number; w: number; h: number } | null): void;
  scrollToY(y: number, ms: number): Promise<void>;
  pos(): { x: number; y: number };
}

declare global {
  interface Window {
    __demo: DemoApi;
  }
}

export function demoRuntime(cfg: RuntimeConfig): void {
  // Re-entrancy guard: addInitScript fires per document, but never re-run within one document.
  if (window.__demo) return;

  const Z = 2147483000; // above every app layer (the app caps around z-100)
  const accent = cfg.accent || "#7c9cff";
  const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

  // A single host layer that owns the cursor, captions, ripples and the spotlight. Appended to
  // <html> (which exists at document-start even before <body>) so it never blocks app paint.
  const host = document.createElement("div");
  host.id = "__demo_layer";
  host.style.cssText =
    `position:fixed;inset:0;z-index:${Z};pointer-events:none;` +
    `font-family:Inter,-apple-system,Segoe UI,system-ui,sans-serif;`;
  // addInitScript runs at document-start, when <html>/<body> may not exist yet — guard against null and
  // re-mount on DOMContentLoaded. Every public method also calls mount() so the layer is always attached
  // by the time it's used.
  const mount = () => {
    const root = document.body || document.documentElement;
    if (root && !root.contains(host)) root.appendChild(host);
  };
  mount();
  document.addEventListener("DOMContentLoaded", mount);

  // ---- cursor ----------------------------------------------------------------
  const cursor = document.createElement("div");
  cursor.style.cssText =
    "position:fixed;left:0;top:0;width:26px;height:26px;will-change:transform;" +
    "transform:translate(-50%,-50%);filter:drop-shadow(0 2px 4px rgba(0,0,0,.55));" +
    "transition:opacity .25s ease;opacity:0;";
  cursor.innerHTML =
    `<svg viewBox="0 0 24 24" width="26" height="26" fill="#fff" stroke="#0b0b0d" ` +
    `stroke-width="1.2" stroke-linejoin="round"><path d="M4 2l6.5 17 2.2-6.7 6.8-2.3z"/></svg>`;
  host.appendChild(cursor);

  let cx = window.innerWidth / 2;
  let cy = window.innerHeight * 0.92;
  const paint = () => {
    cursor.style.transform = `translate(${cx}px,${cy}px) translate(-50%,-50%)`;
  };
  paint();

  function moveCursor(x: number, y: number, ms: number): Promise<void> {
    mount();
    cursor.style.opacity = "1";
    const sx = cx;
    const sy = cy;
    const dur = Math.max(1, ms);
    const start = performance.now();
    return new Promise<void>((resolve) => {
      const tick = (now: number) => {
        const k = Math.min(1, (now - start) / dur);
        const e = ease(k);
        cx = sx + (x - sx) * e;
        cy = sy + (y - sy) * e;
        paint();
        if (k < 1) requestAnimationFrame(tick);
        else resolve();
      };
      requestAnimationFrame(tick);
    });
  }

  function ripple(): void {
    mount();
    const r = document.createElement("div");
    r.style.cssText =
      `position:fixed;left:${cx}px;top:${cy}px;width:14px;height:14px;border-radius:50%;` +
      `transform:translate(-50%,-50%);border:2px solid ${accent};opacity:.9;` +
      `pointer-events:none;`;
    host.appendChild(r);
    const anim = r.animate(
      [
        { transform: "translate(-50%,-50%) scale(.4)", opacity: 0.9 },
        { transform: "translate(-50%,-50%) scale(3.2)", opacity: 0 },
      ],
      { duration: 520, easing: "ease-out" },
    );
    anim.finished.then(() => r.remove()).catch(() => r.remove());
  }

  // ---- caption (lower third) -------------------------------------------------
  const cap = document.createElement("div");
  cap.style.cssText =
    "position:fixed;left:50%;bottom:6.5%;transform:translateX(-50%);max-width:78vw;" +
    "padding:14px 26px;border-radius:14px;background:rgba(12,12,16,.72);" +
    "backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);" +
    `border:1px solid ${accent}40;box-shadow:0 8px 30px rgba(0,0,0,.45);` +
    "color:#f3f5fb;font-size:26px;line-height:1.35;font-weight:500;text-align:center;" +
    "letter-spacing:.2px;opacity:0;transition:opacity .4s ease,transform .4s ease;";
  host.appendChild(cap);

  function caption(text: string): void {
    mount();
    cap.textContent = text;
    cap.style.opacity = "1";
    cap.style.transform = "translateX(-50%) translateY(0)";
  }
  function clearCaption(): void {
    cap.style.opacity = "0";
    cap.style.transform = "translateX(-50%) translateY(8px)";
  }

  // ---- spotlight -------------------------------------------------------------
  const spot = document.createElement("div");
  spot.style.cssText =
    "position:fixed;left:0;top:0;width:0;height:0;border-radius:12px;opacity:0;" +
    "box-shadow:0 0 0 100vmax rgba(8,8,12,.62);transition:all .4s ease;";
  host.appendChild(spot);

  function spotlight(rect: { x: number; y: number; w: number; h: number } | null): void {
    mount();
    if (!rect) {
      spot.style.opacity = "0";
      return;
    }
    const pad = 8;
    spot.style.left = `${rect.x - pad}px`;
    spot.style.top = `${rect.y - pad}px`;
    spot.style.width = `${rect.w + pad * 2}px`;
    spot.style.height = `${rect.h + pad * 2}px`;
    spot.style.opacity = "1";
  }

  // Eased window scroll to an absolute Y (clamped to the scrollable range). Returns a Promise the
  // driver awaits, so a tour can scroll a page at a controlled, human pace.
  function scrollToY(y: number, ms: number): Promise<void> {
    const startY = window.scrollY;
    const maxY = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
    const target = Math.max(0, Math.min(y, maxY));
    const dur = Math.max(1, ms);
    const start = performance.now();
    return new Promise<void>((resolve) => {
      const tick = (now: number) => {
        const k = Math.min(1, (now - start) / dur);
        window.scrollTo(0, startY + (target - startY) * ease(k));
        if (k < 1) requestAnimationFrame(tick);
        else resolve();
      };
      requestAnimationFrame(tick);
    });
  }

  window.__demo = {
    moveCursor,
    ripple,
    caption,
    clearCaption,
    spotlight,
    scrollToY,
    pos: () => ({ x: cx, y: cy }),
  };
}
