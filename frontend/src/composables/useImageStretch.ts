import { shallowRef } from "vue";
import type { PreviewImage } from "@/types";

// useImageStretch renders a linear 16-bit preview buffer to a canvas with a Siril-style display
// stretch — a black/white point (0–65535) plus optional gamma — applied through a 64K-entry LUT. The
// buffer is decoded once (server-side); every slider move just re-runs the LUT + putImageData, so the
// stretch feels instant. The LUT is rebuilt only when black/white/gamma change.
export function useImageStretch() {
  const image = shallowRef<PreviewImage | null>(null);
  // Allocated lazily on first render so unused instances (e.g. one per idle preview button) cost ~0.
  let lut: Uint8Array | null = null;
  let lutBlack = -1;
  let lutWhite = -1;
  let lutGamma = -1;

  function setImage(p: PreviewImage | null) {
    image.value = p;
  }

  function buildLut(black: number, white: number, gamma: number) {
    if (!lut) lut = new Uint8Array(65536);
    if (black === lutBlack && white === lutWhite && gamma === lutGamma) return;
    const span = Math.max(1, white - black);
    const invGamma = gamma > 0 ? 1 / gamma : 1;
    const linear = gamma === 1;
    for (let v = 0; v < 65536; v++) {
      let n = (v - black) / span;
      n = n < 0 ? 0 : n > 1 ? 1 : n;
      if (!linear) n = Math.pow(n, invGamma);
      lut[v] = (n * 255 + 0.5) | 0;
    }
    lutBlack = black;
    lutWhite = white;
    lutGamma = gamma;
  }

  // render draws the current image onto ctx (whose canvas must be sized w×h) with the given stretch.
  function render(
    ctx: CanvasRenderingContext2D,
    black: number,
    white: number,
    gamma: number,
  ) {
    const p = image.value;
    if (!p) return;
    buildLut(black, white, gamma);
    const tab = lut as Uint8Array;
    const out = ctx.createImageData(p.w, p.h);
    const px = out.data;
    const src = p.data;
    const n = p.w * p.h;
    if (p.c === 1) {
      for (let i = 0, j = 0; i < n; i++, j += 4) {
        const g = tab[src[i]];
        px[j] = g;
        px[j + 1] = g;
        px[j + 2] = g;
        px[j + 3] = 255;
      }
    } else {
      for (let i = 0, j = 0, k = 0; i < n; i++, j += 4, k += p.c) {
        px[j] = tab[src[k]];
        px[j + 1] = tab[src[k + 1]];
        px[j + 2] = tab[src[k + 2]];
        px[j + 3] = 255;
      }
    }
    ctx.putImageData(out, 0, 0);
  }

  return { image, setImage, render };
}
