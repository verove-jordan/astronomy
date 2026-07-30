import type { PreviewImage } from "@/types";

// The engine's binary preview format, decoded once for everyone who needs it: the file viewer and
// the live camera view both receive the same little-endian buffer —
//   [w u32][h u32][c u32][autoLo u16][autoHi u16] then w·h·c u16 samples
// — so the live view inherits the file viewer's stretch, zoom and rendering with no second image
// pipeline to maintain.
export function decodePreviewBuffer(buf: ArrayBuffer): PreviewImage {
  const head = new DataView(buf);
  const w = head.getUint32(0, true);
  const h = head.getUint32(4, true);
  const c = head.getUint32(8, true);
  const autoLo = head.getUint16(12, true);
  const autoHi = head.getUint16(14, true);
  const data = new Uint16Array(buf, 16, w * h * c);
  return { w, h, c, autoLo, autoHi, data };
}

// fetchPreviewBuffer GETs a preview endpoint and decodes it, surfacing the server's JSON error
// message rather than a bare status line.
export async function fetchPreviewBuffer(
  url: string,
  signal?: AbortSignal,
): Promise<PreviewImage> {
  const res = await fetch(url, { signal });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = (await res.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // keep statusText
    }
    throw new Error(message);
  }
  return decodePreviewBuffer(await res.arrayBuffer());
}
