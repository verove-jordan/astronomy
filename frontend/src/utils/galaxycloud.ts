// Decoding the Milky Way point cloud the engine samples.
//
// There is no astronomy here and there is deliberately no per-point work: the record block is handed
// to gl.bufferData exactly as it arrived, and every attribute is read by the GPU straight out of it.
// Two hundred thousand points cost one slice and one upload.
//
// The model itself — arms, disc, bulge, halo, colours, the published numbers behind them — lives in
// internal/scene3d/galaxymodel.go. This file knows only the byte layout.

/** GALAXY_VERSION is the record layout this decoder understands. Sent to the engine, which refuses a
 * mismatch loudly rather than handing over bytes that would be rejected here in silence. */
export const GALAXY_VERSION = 1;

export const GALAXY_MAGIC = "ASTROGXY";
export const GALAXY_HEADER_SIZE = 32;
export const GALAXY_RECORD_SIZE = 10;

// Attribute offsets inside a record: three int16 heliocentric galactic coordinates, three colour
// bytes, one brightness byte.
export const GALAXY_OFF_POS = 0;
export const GALAXY_OFF_RGB = 6;
export const GALAXY_OFF_LUM = 9;

export interface GalaxyCloud {
  count: number;
  /** kpcPerUnit converts a stored position unit to kiloparsec, which is one scene unit. */
  kpcPerUnit: number;
  /** records is the record block alone, ready to upload untouched. */
  records: Uint8Array;
}

/**
 * decodeGalaxyCloud validates the header and slices out the records.
 *
 * Null on anything unexpected — a version or record size this build does not know included. Refusing
 * is the only safe answer: reading a layout that has moved would place the Galaxy somewhere plausible
 * and wrong, which is far worse than not drawing it.
 */
export function decodeGalaxyCloud(buf: ArrayBuffer): GalaxyCloud | null {
  if (buf.byteLength < GALAXY_HEADER_SIZE) return null;
  const head = new DataView(buf);
  for (let i = 0; i < GALAXY_MAGIC.length; i++) {
    if (head.getUint8(i) !== GALAXY_MAGIC.charCodeAt(i)) return null;
  }
  if (head.getUint16(8, true) !== GALAXY_VERSION) return null;
  if (head.getUint16(10, true) !== GALAXY_RECORD_SIZE) return null;

  const count = head.getUint32(12, true);
  const pcPerUnit = head.getFloat32(16, true);
  if (!(count > 0) || !(pcPerUnit > 0)) return null;
  const need = GALAXY_HEADER_SIZE + count * GALAXY_RECORD_SIZE;
  if (buf.byteLength < need) return null;

  return {
    count,
    kpcPerUnit: pcPerUnit / 1000,
    records: new Uint8Array(
      buf,
      GALAXY_HEADER_SIZE,
      count * GALAXY_RECORD_SIZE,
    ),
  };
}

/**
 * galaxyPoint reads one point back out, in heliocentric galactic kiloparsecs. The renderer never calls
 * it — the GPU reads the buffer directly — but the tests do, and so would any future readout that
 * needs to name what the cursor is over.
 */
export function galaxyPoint(
  cloud: GalaxyCloud,
  i: number,
): {
  x: number;
  y: number;
  z: number;
  rgb: [number, number, number];
  lum: number;
} | null {
  if (i < 0 || i >= cloud.count) return null;
  const at = i * GALAXY_RECORD_SIZE;
  const v = new DataView(
    cloud.records.buffer,
    cloud.records.byteOffset + at,
    GALAXY_RECORD_SIZE,
  );
  return {
    x: v.getInt16(GALAXY_OFF_POS, true) * cloud.kpcPerUnit,
    y: v.getInt16(GALAXY_OFF_POS + 2, true) * cloud.kpcPerUnit,
    z: v.getInt16(GALAXY_OFF_POS + 4, true) * cloud.kpcPerUnit,
    rgb: [
      v.getUint8(GALAXY_OFF_RGB),
      v.getUint8(GALAXY_OFF_RGB + 1),
      v.getUint8(GALAXY_OFF_RGB + 2),
    ],
    lum: v.getUint8(GALAXY_OFF_LUM) / 255,
  };
}
