import { describe, expect, it } from "vitest";
import {
  GALAXY_HEADER_SIZE,
  GALAXY_RECORD_SIZE,
  GALAXY_VERSION,
  decodeGalaxyCloud,
  galaxyPoint,
} from "@/utils/galaxycloud";

// build makes a cloud the way the engine does, so the decoder is tested against the format rather than
// against a fixture that could drift from it.
function build(
  points: {
    x: number;
    y: number;
    z: number;
    rgb: [number, number, number];
    lum: number;
  }[],
  opts: {
    version?: number;
    recordSize?: number;
    pcPerUnit?: number;
    magic?: string;
  } = {},
): ArrayBuffer {
  const buf = new ArrayBuffer(
    GALAXY_HEADER_SIZE + points.length * GALAXY_RECORD_SIZE,
  );
  const v = new DataView(buf);
  const magic = opts.magic ?? "ASTROGXY";
  for (let i = 0; i < 8; i++) v.setUint8(i, magic.charCodeAt(i));
  v.setUint16(8, opts.version ?? GALAXY_VERSION, true);
  v.setUint16(10, opts.recordSize ?? GALAXY_RECORD_SIZE, true);
  v.setUint32(12, points.length, true);
  v.setFloat32(16, opts.pcPerUnit ?? 2, true);
  points.forEach((p, i) => {
    const at = GALAXY_HEADER_SIZE + i * GALAXY_RECORD_SIZE;
    const q = opts.pcPerUnit ?? 2;
    v.setInt16(at, Math.round((p.x * 1000) / q), true);
    v.setInt16(at + 2, Math.round((p.y * 1000) / q), true);
    v.setInt16(at + 4, Math.round((p.z * 1000) / q), true);
    v.setUint8(at + 6, p.rgb[0]);
    v.setUint8(at + 7, p.rgb[1]);
    v.setUint8(at + 8, p.rgb[2]);
    v.setUint8(at + 9, Math.round(p.lum * 255));
  });
  return buf;
}

const SAMPLE = [
  {
    x: 8.15,
    y: 0,
    z: -0.02,
    rgb: [255, 210, 160] as [number, number, number],
    lum: 0.55,
  },
  {
    x: -6,
    y: 12.5,
    z: 0.3,
    rgb: [170, 200, 255] as [number, number, number],
    lum: 1,
  },
];

describe("decoding the galaxy cloud", () => {
  it("slices out the records without copying or unpacking them", () => {
    const cloud = decodeGalaxyCloud(build(SAMPLE));
    expect(cloud).not.toBeNull();
    expect(cloud!.count).toBe(2);
    expect(cloud!.kpcPerUnit).toBeCloseTo(0.002, 12);
    // Exactly the record block — this is handed to gl.bufferData untouched.
    expect(cloud!.records.byteLength).toBe(2 * GALAXY_RECORD_SIZE);
    expect(cloud!.records.byteOffset).toBe(GALAXY_HEADER_SIZE);
  });

  it("reads a point back in heliocentric galactic kiloparsecs", () => {
    const cloud = decodeGalaxyCloud(build(SAMPLE))!;
    const p = galaxyPoint(cloud, 0)!;
    // Quantised at two parsecs, so agreement to a few thousandths of a kiloparsec is exact.
    expect(p.x).toBeCloseTo(8.15, 2);
    expect(p.y).toBeCloseTo(0, 6);
    expect(p.z).toBeCloseTo(-0.02, 2);
    expect(p.rgb).toEqual([255, 210, 160]);
    expect(p.lum).toBeCloseTo(0.55, 2);

    const q = galaxyPoint(cloud, 1)!;
    expect(q.x).toBeCloseTo(-6, 2);
    expect(q.y).toBeCloseTo(12.5, 2);
    expect(galaxyPoint(cloud, 2)).toBeNull();
    expect(galaxyPoint(cloud, -1)).toBeNull();
  });

  it("refuses a layout it does not know rather than misreading it", () => {
    // Reading a moved layout would place the Galaxy somewhere plausible and wrong, which is far worse
    // than not drawing it — so every one of these has to come back null.
    expect(
      decodeGalaxyCloud(build(SAMPLE, { version: GALAXY_VERSION + 1 })),
    ).toBeNull();
    expect(decodeGalaxyCloud(build(SAMPLE, { recordSize: 12 }))).toBeNull();
    expect(decodeGalaxyCloud(build(SAMPLE, { magic: "NOTACLOU" }))).toBeNull();
    expect(decodeGalaxyCloud(build(SAMPLE, { pcPerUnit: 0 }))).toBeNull();
    expect(decodeGalaxyCloud(new ArrayBuffer(8))).toBeNull();
    expect(decodeGalaxyCloud(build([]))).toBeNull();
    // Truncated mid-transfer: the header promises more records than arrived.
    expect(
      decodeGalaxyCloud(build(SAMPLE).slice(0, GALAXY_HEADER_SIZE + 4)),
    ).toBeNull();
  });
});
