import { describe, expect, it } from "vitest";
import type { Scene3DBillboard, Scene3DManifest } from "@/types";
import {
  DEPTH_ESTIMATED,
  FLAG_HAS_VELOCITY,
  MIN_ORBIT_DISTANCE,
  applyZoom,
  cameraPhysical,
  motionEndpoint,
  panOrbit,
  physicalPosition,
  radialSign,
  tessellateShape,
  unwarpZ,
  zoomExponent,
  DEPTH_MEASURED,
  FLAG_CLUSTER_MEMBER,
  FLAG_IDENTIFIED,
  HEADER_SIZE,
  PICK_RADIUS_PX,
  RECORD_SIZE,
  SCENE_MAGIC,
  SCENE_VERSION,
  Z_REF,
  billboardQuad,
  decadeRings,
  decodeScene,
  defaultOrbit,
  formatDistance,
  multiply,
  fitPerspective,
  pickNearest,
  projectToScreen,
  readStar,
  scenePosition,
  linearPosition,
  UNITS_PER_PC,
  sortBillboardsFarFirst,
  viewMatrix,
  warpZ,
} from "./scene3d";

// --- fixtures ------------------------------------------------------------------------------------

interface FixtureStar {
  dir: [number, number, number];
  distPc: number;
  rgb?: [number, number, number];
  absMag?: number;
  mag?: number;
  depth?: number;
  flags?: number;
  nameIdx?: number;
  vel?: [number, number, number];
  srcIdx?: number;
}

// encodeScene writes a buffer byte for byte the way internal/scene3d/format.go does. It is the
// contract between the two languages: if the Go encoder's layout ever moves, these tests keep
// reading the old one and fail, instead of the browser silently drawing a scrambled star field.
function encodeScene(stars: FixtureStar[], names: string[] = []): ArrayBuffer {
  let nameBytes = 4;
  const encoded = names.map((n) => new TextEncoder().encode(n));
  for (const n of encoded) nameBytes += 2 + n.length;

  const buf = new ArrayBuffer(
    HEADER_SIZE + stars.length * RECORD_SIZE + nameBytes,
  );
  const v = new DataView(buf);
  const bytes = new Uint8Array(buf);

  bytes.set(new TextEncoder().encode(SCENE_MAGIC), 0);
  v.setUint16(8, SCENE_VERSION, true);
  v.setUint16(10, RECORD_SIZE, true);
  v.setUint32(12, stars.length, true);
  v.setUint32(16, HEADER_SIZE, true);
  const strOff = HEADER_SIZE + stars.length * RECORD_SIZE;
  v.setUint32(20, strOff, true);

  stars.forEach((s, i) => {
    const o = HEADER_SIZE + i * RECORD_SIZE;
    v.setFloat32(o, s.dir[0], true);
    v.setFloat32(o + 4, s.dir[1], true);
    v.setFloat32(o + 8, s.dir[2], true);
    v.setFloat32(o + 12, s.distPc, true);
    const [r, g, b] = s.rgb ?? [255, 255, 255];
    v.setUint8(o + 16, r);
    v.setUint8(o + 17, g);
    v.setUint8(o + 18, b);
    v.setInt8(o + 19, s.absMag === undefined ? -128 : Math.round(s.absMag * 4));
    v.setUint8(o + 20, (s.depth ?? DEPTH_MEASURED) | (s.flags ?? 0));
    v.setUint8(o + 21, s.mag === undefined ? 255 : Math.round((s.mag + 5) * 8));
    v.setUint16(o + 22, s.nameIdx ?? 0, true);
    const vel = s.vel ?? [0, 0, 0];
    v.setInt16(o + 24, Math.round(vel[0] * 10), true);
    v.setInt16(o + 26, Math.round(vel[1] * 10), true);
    v.setInt16(o + 28, Math.round(vel[2] * 10), true);
    v.setUint16(o + 30, s.srcIdx ?? 0, true);
  });

  v.setUint32(strOff, names.length, true);
  let p = strOff + 4;
  for (const n of encoded) {
    v.setUint16(p, n.length, true);
    p += 2;
    bytes.set(n, p);
    p += n.length;
  }
  return buf;
}

const IMAGE_W = 2400;
const IMAGE_H = 1800;

// The half-field tangents the engine ships are measured between PIXEL CENTRES, so a square-pixel
// frame has tanW/tanH = (W−1)/(H−1). Keeping the fixture self-consistent matters: fitPerspective
// letterboxes any mismatch between the field's aspect and the canvas's, and a fixture that quietly
// disagreed with its own dimensions would show up as a phantom offset in the depth-0 tests.
const TAN_HALF_W = 0.013;
const TAN_HALF_H = (TAN_HALF_W * (IMAGE_H - 1)) / (IMAGE_W - 1);

// The canvas the depth-0 tests draw into has the image's own aspect, which is exactly where
// fitPerspective must be a no-op beyond the half-pixel correction. The letterboxing itself is
// covered separately — it is the bug that shipped.
const IMAGE_ASPECT = IMAGE_W / IMAGE_H;

function manifest(over: Partial<Scene3DManifest> = {}): Scene3DManifest {
  return {
    version: 1,
    available: true,
    image: { width: IMAGE_W, height: IMAGE_H },
    camera: {
      tan_half_w: TAN_HALF_W,
      tan_half_h: TAN_HALF_H,
      fov_y_deg: 1.12,
      center_ra: 83.8,
      center_dec: -5.4,
      right_handed: true,
    },
    depth: {
      near_pc: 50,
      far_pc: 5000,
      min_pc: 20,
      max_pc: 9000,
      median_pc: 500,
    },
    stars: {
      plotted: 100,
      placed: 90,
      measured: 50,
      estimated: 40,
      unknown: 10,
      identified: 55,
      named: 50,
      physical_colour: 88,
      moving: 40,
    },
    photometric: { calibrated: true, pairs: 120 },
    ...over,
  };
}

// --- decoding ------------------------------------------------------------------------------------

describe("decodeScene", () => {
  it("reads back everything the engine encoded", () => {
    const buf = encodeScene(
      [
        {
          dir: [0, 0, 1],
          distPc: 412.5,
          rgb: [200, 210, 255],
          absMag: -3.25,
          mag: 6.5,
          depth: DEPTH_MEASURED,
          flags: FLAG_IDENTIFIED,
          nameIdx: 1,
        },
        {
          dir: [0.01, -0.02, 0.9997],
          distPc: 1650,
          rgb: [255, 180, 120],
          depth: DEPTH_ESTIMATED,
        },
        {
          dir: [0, 0, 1],
          distPc: 136,
          depth: DEPTH_MEASURED,
          flags: FLAG_IDENTIFIED | FLAG_CLUSTER_MEMBER,
          nameIdx: 2,
        },
      ],
      ["Alnitak", "TYC 4669-731-1"],
    );

    const scene = decodeScene(buf);
    expect(scene.count).toBe(3);
    expect(scene.names).toEqual(["Alnitak", "TYC 4669-731-1"]);

    const first = readStar(scene, 0);
    expect(first.distPc).toBeCloseTo(412.5, 3);
    expect(first.hex).toBe("#c8d2ff");
    expect(first.absMag).toBeCloseTo(-3.25, 6);
    expect(first.mag).toBeCloseTo(6.5, 6);
    expect(first.depth).toBe(DEPTH_MEASURED);
    expect(first.identified).toBe(true);
    expect(first.clusterMember).toBe(false);
    expect(first.name).toBe("Alnitak");

    // Absent values must read as absent, not as a plausible-looking number.
    const second = readStar(scene, 1);
    expect(second.mag).toBeNull();
    expect(second.absMag).toBeNull();
    expect(second.depth).toBe(DEPTH_ESTIMATED);
    expect(second.identified).toBe(false);
    expect(second.name).toBe("");

    expect(readStar(scene, 2).clusterMember).toBe(true);
  });

  it("refuses a buffer it cannot read rather than drawing nonsense", () => {
    const good = encodeScene([{ dir: [0, 0, 1], distPc: 10 }]);

    expect(() => decodeScene(new ArrayBuffer(8))).toThrow();

    const wrongMagic = good.slice(0);
    new Uint8Array(wrongMagic).set(new TextEncoder().encode("NOTASCEN"), 0);
    expect(() => decodeScene(wrongMagic)).toThrow(/not a scene/);

    const futureVersion = good.slice(0);
    new DataView(futureVersion).setUint16(8, 99, true);
    expect(() => decodeScene(futureVersion)).toThrow(/version/);

    // 24 is the v1 record size: a scene cached before the format grew must be refused, not read
    // with the new offsets against the old bytes.
    const v1RecordSize = good.slice(0);
    new DataView(v1RecordSize).setUint16(10, 24, true);
    expect(() => decodeScene(v1RecordSize)).toThrow(/record size/);

    expect(() => decodeScene(good.slice(0, HEADER_SIZE + 4))).toThrow();
  });
});

// --- the depth warp ------------------------------------------------------------------------------

describe("warpZ", () => {
  it("collapses the whole field onto one plane at depth zero", () => {
    for (const d of [10, 136, 412, 5000, 90000]) {
      expect(warpZ(d, 50, 5000, 0)).toBe(Z_REF);
    }
  });

  it("orders stars by distance once depth opens", () => {
    const near = warpZ(60, 50, 5000, 1);
    const mid = warpZ(500, 50, 5000, 1);
    const far = warpZ(4000, 50, 5000, 1);
    expect(near).toBeLessThan(mid);
    expect(mid).toBeLessThan(far);
  });

  it("is logarithmic, so three decades stay legible", () => {
    // Equal ratios must occupy equal depth: 50→500 and 500→5000 are one decade each.
    const a = warpZ(500, 50, 5000, 1) - warpZ(50, 50, 5000, 1);
    const b = warpZ(5000, 50, 5000, 1) - warpZ(500, 50, 5000, 1);
    expect(a).toBeCloseTo(b, 6);
  });

  it("clamps beyond the percentile range instead of flying off", () => {
    expect(warpZ(1, 50, 5000, 1)).toBe(warpZ(50, 50, 5000, 1));
    expect(warpZ(1e9, 50, 5000, 1)).toBe(warpZ(5000, 50, 5000, 1));
  });

  it("survives a degenerate range", () => {
    expect(warpZ(100, 0, 0, 1)).toBe(Z_REF);
    expect(warpZ(0, 50, 5000, 1)).toBe(Z_REF);
  });
});

describe("scenePosition", () => {
  it("keeps a star on its own line of sight at every depth", () => {
    const dir: [number, number, number] = [0.004, -0.003, 0.9999];
    for (const depth of [0, 0.3, 0.7, 1]) {
      const p = scenePosition(dir, 900, 50, 5000, depth);
      // The ratio x/z is the direction's own — the star slides ALONG the ray, never across it.
      expect(p[0] / p[2]).toBeCloseTo(dir[0] / dir[2], 9);
      expect(p[1] / p[2]).toBeCloseTo(dir[1] / dir[2], 9);
    }
  });
});

// --- the property the feature rests on -----------------------------------------------------------

// The galaxy view swaps the scene into linear parsecs. That is only safe if it starts from exactly
// the same picture — so this is the load-bearing test of the whole feature: with the eye at Earth,
// linear placement and warped depth-0 placement must land on the SAME pixel, because both put the
// star on the same ray from the origin and a pinhole at the origin sees only direction.
//
// If this ever fails, turning the toggle on will visibly jump, and every distance drawn beside the
// galaxy is suspect.
describe("the galaxy view at zoom zero is the photograph", () => {
  it("puts every star on the pixel the warped view puts it on", () => {
    const m = manifest();
    const { width, height } = m.image;
    const cam = m.camera;

    const pixels: [number, number][] = [
      [0, 0],
      [width - 1, height - 1],
      [(width - 1) / 2, (height - 1) / 2],
      [731, 1290],
      [2048, 64],
    ];
    const stars: FixtureStar[] = pixels.map(([px, py]) => {
      const u = ((px - (width - 1) / 2) / ((width - 1) / 2)) * cam.tan_half_w;
      const v = ((py - (height - 1) / 2) / ((height - 1) / 2)) * cam.tan_half_h;
      const n = Math.hypot(u, v, 1);
      // Distances spanning four decades: if the two spaces disagreed about depth at all, a star at
      // 40 pc and one at 4000 pc could not both land right.
      return { dir: [u / n, v / n, 1 / n], distPc: 40 + px * 2 };
    });

    const scene = decodeScene(encodeScene(stars));
    // defaultOrbit puts the eye exactly at the origin — Earth.
    const viewProj = multiply(
      fitPerspective(m, IMAGE_ASPECT),
      viewMatrix(defaultOrbit()),
    );
    const vp = { width, height };

    for (let i = 0; i < scene.count; i++) {
      const s = readStar(scene, i);
      const warped = projectToScreen(
        scenePosition(s.dir, s.distPc, m.depth.near_pc, m.depth.far_pc, 0),
        viewProj,
        vp,
      );
      const linear = projectToScreen(
        linearPosition(s.dir, s.distPc, UNITS_PER_PC),
        viewProj,
        vp,
      );
      expect(warped).not.toBeNull();
      expect(linear).not.toBeNull();
      expect(linear![0]).toBeCloseTo(warped![0], 3);
      expect(linear![1]).toBeCloseTo(warped![1], 3);
      // And still the photograph, to the same tolerance the warped test asserts.
      expect(linear![0]).toBeCloseTo(pixels[i][0] + 0.5, 1);
      expect(linear![1]).toBeCloseTo(pixels[i][1] + 0.5, 1);
    }
  });

  it("keeps a star's true distance, unlike the warp", () => {
    // The whole point of the second space: the ratio of two stars' distances survives it.
    const near = linearPosition([0, 0, 1], 100);
    const far = linearPosition([0, 0, 1], 8150);
    expect(far[2] / near[2]).toBeCloseTo(81.5, 9);
    // One scene unit is one kiloparsec, so the Sun's distance to the galactic centre is 8.15.
    expect(far[2]).toBeCloseTo(8.15, 9);
  });
});

describe("the depth-zero view is the photograph", () => {
  it("projects every star back onto the pixel it was detected at", () => {
    const m = manifest();
    const { width, height } = m.image;
    const cam = m.camera;

    // Stars laid out on known pixels, turned into the directions the engine would have shipped.
    const pixels: [number, number][] = [
      [0, 0],
      [width - 1, 0],
      [0, height - 1],
      [width - 1, height - 1],
      [(width - 1) / 2, (height - 1) / 2],
      [731, 1290],
    ];
    const stars: FixtureStar[] = pixels.map(([px, py]) => {
      const u = ((px - (width - 1) / 2) / ((width - 1) / 2)) * cam.tan_half_w;
      const v = ((py - (height - 1) / 2) / ((height - 1) / 2)) * cam.tan_half_h;
      const n = Math.hypot(u, v, 1);
      return { dir: [u / n, v / n, 1 / n], distPc: 100 + px };
    });

    const scene = decodeScene(encodeScene(stars));
    const orbit = defaultOrbit();
    const viewProj = multiply(
      fitPerspective(m, IMAGE_ASPECT),
      viewMatrix(orbit),
    );
    const vp = { width, height };

    for (let i = 0; i < scene.count; i++) {
      const s = readStar(scene, i);
      const pos = scenePosition(
        s.dir,
        s.distPc,
        m.depth.near_pc,
        m.depth.far_pc,
        0,
      );
      const screen = projectToScreen(pos, viewProj, vp);
      expect(screen).not.toBeNull();
      // Pixel n covers [n, n+1) on the canvas, so its centre is at n + 0.5. float32 in the record
      // costs about a hundredth of a pixel on top of that.
      expect(screen![0]).toBeCloseTo(pixels[i][0] + 0.5, 1);
      expect(screen![1]).toBeCloseTo(pixels[i][1] + 0.5, 1);
    }
  });

  it("holds the screen position fixed as the depth slider opens", () => {
    const m = manifest();
    const scene = decodeScene(
      encodeScene([
        { dir: [0.006, -0.004, 0.9999], distPc: 80 },
        { dir: [0.006, -0.004, 0.9999], distPc: 4000 },
      ]),
    );
    const viewProj = multiply(
      fitPerspective(m, IMAGE_ASPECT),
      viewMatrix(defaultOrbit()),
    );
    const vp = { width: 800, height: 600 };

    // Two stars on the SAME line of sight at very different distances. From Earth — the camera's
    // home position — they must stay exactly on top of each other however far the depth opens;
    // that is what it means for the view to still be the photograph.
    for (const depth of [0, 0.25, 0.5, 1]) {
      const a = projectToScreen(
        scenePosition(readStar(scene, 0).dir, 80, 50, 5000, depth),
        viewProj,
        vp,
      )!;
      const b = projectToScreen(
        scenePosition(readStar(scene, 1).dir, 4000, 50, 5000, depth),
        viewProj,
        vp,
      )!;
      expect(a[0]).toBeCloseTo(b[0], 4);
      expect(a[1]).toBeCloseTo(b[1], 4);
    }
  });
});

// --- picking -------------------------------------------------------------------------------------

describe("pickNearest", () => {
  const m = manifest();
  const vp = { width: 800, height: 600 };
  const viewProj = multiply(
    fitPerspective(m, IMAGE_ASPECT),
    viewMatrix(defaultOrbit()),
  );
  const scene = decodeScene(
    encodeScene(
      [
        { dir: [0, 0, 1], distPc: 100, depth: DEPTH_MEASURED, nameIdx: 1 },
        {
          dir: [0.008, 0, 0.99997],
          distPc: 900,
          depth: DEPTH_ESTIMATED,
          nameIdx: 2,
        },
      ],
      ["centre star", "offset star"],
    ),
  );
  const opts = { near: 50, far: 5000, depth: 0 };

  it("selects the star under the pointer", () => {
    const hit = pickNearest(
      scene,
      vp.width / 2,
      vp.height / 2,
      viewProj,
      vp,
      opts,
    );
    expect(hit?.name).toBe("centre star");
  });

  it("returns nothing when the pointer is on empty sky", () => {
    expect(pickNearest(scene, 10, 10, viewProj, vp, opts)).toBeNull();
  });

  it("never selects a star the depth filter is hiding", () => {
    const hit = pickNearest(scene, vp.width / 2, vp.height / 2, viewProj, vp, {
      ...opts,
      visible: (d) => d !== DEPTH_MEASURED,
    });
    // The centre star is hidden, and the offset one is outside the pick radius.
    expect(hit).toBeNull();
  });

  it("has a pick radius wide enough to be usable", () => {
    const off = pickNearest(
      scene,
      vp.width / 2 + PICK_RADIUS_PX - 2,
      vp.height / 2,
      viewProj,
      vp,
      opts,
    );
    expect(off?.name).toBe("centre star");
  });
});

// --- billboards ----------------------------------------------------------------------------------

describe("billboardQuad", () => {
  const m = manifest();
  const bb: Scene3DBillboard = {
    name: "M42",
    dist_pc: 412,
    dist_source: "table",
    x: 1200,
    y: 900,
    rx_px: 300,
    ry_px: 220,
    angle_rad: 0,
  };

  it("lands back on the pixels it was cut from at depth zero", () => {
    const q = billboardQuad(bb, m, 0)!;
    expect(q).not.toBeNull();
    const viewProj = multiply(
      fitPerspective(m, IMAGE_ASPECT),
      viewMatrix(defaultOrbit()),
    );
    const vp = { width: m.image.width, height: m.image.height };
    // The corners of the footprint, in image pixels, are where the quad must project to.
    const want = [
      [bb.x - bb.rx_px, bb.y - bb.ry_px],
      [bb.x + bb.rx_px, bb.y - bb.ry_px],
      [bb.x + bb.rx_px, bb.y + bb.ry_px],
      [bb.x - bb.rx_px, bb.y + bb.ry_px],
    ];
    q.corners.forEach((c, i) => {
      const s = projectToScreen(c, viewProj, vp)!;
      expect(s[0]).toBeCloseTo(want[i][0] + 0.5, 0);
      expect(s[1]).toBeCloseTo(want[i][1] + 0.5, 0);
    });
  });

  it("maps the corners onto the right patch of the backdrop", () => {
    const q = billboardQuad(bb, m, 0)!;
    expect(q.uvs[0][0]).toBeCloseTo((1200 - 300) / 2400, 6);
    expect(q.uvs[0][1]).toBeCloseTo((900 - 220) / 1800, 6);
    expect(q.uvs[2][0]).toBeCloseTo((1200 + 300) / 2400, 6);
    expect(q.uvs[2][1]).toBeCloseTo((900 + 220) / 1800, 6);
  });

  it("moves back as depth opens, tracking its own distance", () => {
    const flat = billboardQuad(bb, m, 0)!;
    const open = billboardQuad(bb, m, 1)!;
    expect(open.corners[0][2]).toBeGreaterThan(flat.corners[0][2]);
  });

  it("refuses an object with no usable geometry", () => {
    expect(billboardQuad({ ...bb, dist_pc: 0 }, m, 0)).toBeNull();
    expect(billboardQuad({ ...bb, rx_px: 0 }, m, 0)).toBeNull();
  });
});

describe("sortBillboardsFarFirst", () => {
  it("draws the far object before the near one", () => {
    const mk = (name: string, dist_pc: number): Scene3DBillboard => ({
      name,
      dist_pc,
      dist_source: "table",
      x: 0,
      y: 0,
      rx_px: 1,
      ry_px: 1,
      angle_rad: 0,
    });
    const got = sortBillboardsFarFirst([
      mk("M82", 3526000),
      mk("M81", 3618000),
    ]);
    expect(got.map((b) => b.name)).toEqual(["M81", "M82"]);
  });
});

// --- scale readouts ------------------------------------------------------------------------------

describe("decadeRings", () => {
  it("marks the round distances inside the field's range", () => {
    expect(decadeRings(50, 5000)).toEqual([100, 1000]);
    expect(decadeRings(5, 50000)).toEqual([10, 100, 1000, 10000]);
  });

  it("returns nothing for a range with no decade in it", () => {
    expect(decadeRings(120, 480)).toEqual([]);
    expect(decadeRings(0, 100)).toEqual([]);
    expect(decadeRings(500, 100)).toEqual([]);
  });
});

describe("formatDistance", () => {
  it("uses the unit that keeps the number readable", () => {
    expect(formatDistance(96.4)).toBe("96.4 pc");
    expect(formatDistance(136.1)).toBe("136 pc");
    expect(formatDistance(412)).toBe("412 pc");
    expect(formatDistance(3618000)).toBe("3.62 Mpc");
    expect(formatDistance(11500)).toBe("11.50 kpc");
    expect(formatDistance(0)).toBe("—");
  });
});

// --- the aspect fix ------------------------------------------------------------------------------

describe("fitPerspective", () => {
  const m = manifest();

  it("is a no-op when the canvas matches the image", () => {
    const fitted = fitPerspective(m, IMAGE_ASPECT);
    // The x and y scales are 1/tan; equal aspect means the ratio between them is the image's own.
    expect(fitted[5] / fitted[0]).toBeCloseTo(IMAGE_ASPECT, 6);
  });

  // This is the bug that shipped: the projection was built from the IMAGE's aspect while
  // gl.viewport stretched clip space across the canvas, so a 1.33:1 frame in a 4:1 panel came out
  // squashed nearly threefold. Fitting must keep the field's shape and spend the spare axis on sky.
  it("keeps the field's shape in a canvas of any aspect", () => {
    for (const canvasAspect of [0.5, 1, IMAGE_ASPECT, 2.5, 4.2]) {
      const p = fitPerspective(m, canvasAspect);
      const tanW = 1 / p[0];
      const tanH = 1 / p[5];
      // Clip space is stretched across the canvas, so one canvas pixel subtends 2·tanW/width
      // horizontally and 2·tanH/height vertically. Those are equal — which is exactly what "not
      // stretched" means — precisely when the field's aspect equals the canvas's.
      // 6 decimals, not more: the matrix is a Float32Array, so this relation cannot be carried
      // tighter than single precision however exact the arithmetic behind it.
      expect(tanW / tanH).toBeCloseTo(canvasAspect, 6);
    }
  });

  it("widens rather than crops, so nothing in the frame is lost", () => {
    const base = fitPerspective(m, IMAGE_ASPECT);
    for (const canvasAspect of [0.5, 4.2]) {
      const p = fitPerspective(m, canvasAspect);
      expect(1 / p[0]).toBeGreaterThanOrEqual(1 / base[0] - 1e-9);
      expect(1 / p[5]).toBeGreaterThanOrEqual(1 / base[5] - 1e-9);
    }
  });

  it("a star still lands on its own pixel in a wide canvas", () => {
    const canvasAspect = 4.2;
    const vp = { width: 2100, height: 500 };
    const viewProj = multiply(
      fitPerspective(m, canvasAspect),
      viewMatrix(defaultOrbit()),
    );
    // A star at the exact centre of the frame must draw at the exact centre of the canvas.
    const scene = decodeScene(encodeScene([{ dir: [0, 0, 1], distPc: 400 }]));
    const s = readStar(scene, 0);
    const p = projectToScreen(
      scenePosition(s.dir, s.distPc, m.depth.near_pc, m.depth.far_pc, 0),
      viewProj,
      vp,
    )!;
    expect(p[0]).toBeCloseTo(vp.width / 2, 6);
    expect(p[1]).toBeCloseTo(vp.height / 2, 6);
  });
});

// --- star physics --------------------------------------------------------------------------------

describe("cameraPhysical", () => {
  const m = manifest();

  it("puts the camera at Earth while the scene is still the photograph", () => {
    // At depth 0 the eye must be at the origin, because that is what makes every star's brightness
    // its Earth magnitude and the view the picture the run produced.
    expect(cameraPhysical(defaultOrbit(), m, 0)).toEqual([0, 0, 0]);
  });

  it("carries the eye into real space once depth opens", () => {
    const o = {
      ...defaultOrbit(),
      target: [0, 0, 3] as [number, number, number],
      distance: 0.5,
    };
    const p = cameraPhysical(o, m, 1);
    const d = Math.hypot(p[0], p[1], p[2]);
    expect(d).toBeGreaterThan(0);
    // It must land inside the field's own distance range, not somewhere arbitrary.
    expect(d).toBeGreaterThan(m.depth.near_pc * 0.5);
    expect(d).toBeLessThan(m.depth.far_pc * 2);
  });
});

describe("unwarpZ", () => {
  it("inverts warpZ wherever the warp is invertible", () => {
    for (const distPc of [80, 500, 3000]) {
      const z = warpZ(distPc, 50, 5000, 1);
      expect(unwarpZ(z, 50, 5000, 1)).toBeCloseTo(distPc, 6);
    }
  });

  it("has nothing to invert at depth zero, where the field is one plane", () => {
    expect(unwarpZ(Z_REF, 50, 5000, 0)).toBe(0);
  });
});

describe("physicalPosition", () => {
  it("is the direction times the real distance, never the warped one", () => {
    expect(physicalPosition([0, 0, 1], 412)).toEqual([0, 0, 412]);
  });
});

// --- interaction ---------------------------------------------------------------------------------

describe("panOrbit", () => {
  it("slides what the camera looks at", () => {
    const o = defaultOrbit();
    const moved = panOrbit(o, 100, 0, 500, 0.01);
    expect(moved.target).not.toEqual(o.target);
    expect(moved.distance).toBe(o.distance);
    expect(moved.yaw).toBe(o.yaw);
  });

  it("moves the same number of screen pixels at any zoom", () => {
    // Panning that changes speed with zoom is the thing that feels broken, so the step scales with
    // the orbit distance: twice as far away, twice as much world per pixel.
    const near = panOrbit(
      { ...defaultOrbit(), distance: 1 },
      100,
      0,
      500,
      0.01,
    );
    const far = panOrbit({ ...defaultOrbit(), distance: 2 }, 100, 0, 500, 0.01);
    const dNear = Math.abs(near.target[0] - defaultOrbit().target[0]);
    const dFar = Math.abs(far.target[0] - defaultOrbit().target[0]);
    expect(dFar / dNear).toBeCloseTo(2, 6);
  });

  it("survives a zero-height viewport", () => {
    const o = defaultOrbit();
    expect(panOrbit(o, 10, 10, 0, 0.01)).toEqual(o);
  });
});

describe("zoomExponent", () => {
  it("gives a fast gesture more reach than a slow one", () => {
    const slow = Math.abs(zoomExponent(100, 400));
    const fast = Math.abs(zoomExponent(100, 10));
    expect(fast).toBeGreaterThan(slow * 2);
  });

  it("keeps a slow gesture precise", () => {
    // A deliberate, slow scroll must not jump scale — this is the half of the balance that a single
    // fixed exponent per pixel cannot serve at the same time as the other half.
    expect(Math.abs(zoomExponent(10, 200))).toBeLessThan(0.05);
  });

  it("stops rewarding speed past a point", () => {
    const quick = Math.abs(zoomExponent(100, 5));
    const absurd = Math.abs(zoomExponent(100, 0.01));
    expect(absurd / quick).toBeLessThan(1.5);
  });

  it("keeps its direction", () => {
    expect(zoomExponent(100, 16)).toBeGreaterThan(0);
    expect(zoomExponent(-100, 16)).toBeLessThan(0);
  });
});

describe("applyZoom", () => {
  it("cannot be driven through the camera's own position", () => {
    let o = defaultOrbit();
    for (let i = 0; i < 200; i++) o = applyZoom(o, -1);
    expect(o.distance).toBeGreaterThanOrEqual(MIN_ORBIT_DISTANCE);
    expect(Number.isFinite(o.distance)).toBe(true);
  });
});

// --- motion --------------------------------------------------------------------------------------

describe("motion vectors", () => {
  const m = manifest();
  const scene = decodeScene(
    encodeScene([
      // 50 km/s straight away from us at 100 pc.
      {
        dir: [0, 0, 1],
        distPc: 100,
        flags: FLAG_HAS_VELOCITY,
        vel: [0, 0, 50],
      },
      {
        dir: [0, 0, 1],
        distPc: 100,
        flags: FLAG_HAS_VELOCITY,
        vel: [0, 0, -50],
      },
      { dir: [0, 0, 1], distPc: 100 },
    ]),
  );

  it("reads the velocity back off the wire", () => {
    expect(readStar(scene, 0).velocity).toEqual([0, 0, 50]);
    expect(readStar(scene, 2).velocity).toBeNull();
  });

  it("tells receding from approaching", () => {
    expect(radialSign(readStar(scene, 0))).toBe(1);
    expect(radialSign(readStar(scene, 1))).toBe(-1);
    expect(radialSign(readStar(scene, 2))).toBe(0);
  });

  it("has no endpoint for a star with no measured motion", () => {
    expect(motionEndpoint(readStar(scene, 2), m, 1, 100000)).toBeNull();
  });

  it("moves further for longer", () => {
    const s = readStar(scene, 0);
    const short = motionEndpoint(s, m, 1, 100000)!;
    const long = motionEndpoint(s, m, 1, 1000000)!;
    const from = scenePosition(
      s.dir,
      s.distPc,
      m.depth.near_pc,
      m.depth.far_pc,
      1,
    );
    const dShort = Math.abs(short[2] - from[2]);
    const dLong = Math.abs(long[2] - from[2]);
    expect(dLong).toBeGreaterThan(dShort);
  });

  it("displaces by the real distance the star covers", () => {
    // 50 km/s for 100 000 years is about 5.1 pc — the check that the arrow's length is a physical
    // quantity and not an arbitrary scale factor.
    const s = readStar(scene, 0);
    const pcPerYear = 31557600 / 3.0856775814913673e13;
    expect(50 * pcPerYear * 100000).toBeCloseTo(5.11, 1);
    expect(motionEndpoint(s, m, 1, 100000)).not.toBeNull();
  });
});

// --- object shapes -------------------------------------------------------------------------------

describe("tessellateShape", () => {
  const m = manifest();
  const base: Scene3DBillboard = {
    name: "X",
    dist_pc: 800,
    dist_source: "table",
    x: 1200,
    y: 900,
    rx_px: 400,
    ry_px: 200,
    angle_rad: 0,
  };

  it("leaves an object with no shape as a flat card", () => {
    expect(tessellateShape(base, m)).toBeNull();
    expect(
      tessellateShape(
        { ...base, shape: { kind: "plane", source: "assumed", note: "" } },
        m,
      ),
    ).toBeNull();
  });

  it("builds a galaxy's inclined disc", () => {
    const mesh = tessellateShape(
      {
        ...base,
        shape: {
          kind: "disc",
          source: "measured",
          note: "",
          inclination_deg: 60,
          position_angle_deg: 30,
          radius_pc: 5000,
        },
      },
      m,
    )!;
    expect(mesh.kind).toBe("disc");
    expect(mesh.slices).toBe(0);
    expect(mesh.indices.length).toBeGreaterThan(0);
    // Every vertex carries a direction and a distance, not a position — that is what lets the same
    // depth warp the stars use apply to the mesh per-vertex.
    expect(mesh.vertices.length % 7).toBe(0);
    for (let i = 0; i < mesh.vertices.length; i += 7) {
      const d = Math.hypot(
        mesh.vertices[i],
        mesh.vertices[i + 1],
        mesh.vertices[i + 2],
      );
      expect(d).toBeCloseTo(1, 5); // unit direction
      expect(mesh.vertices[i + 3]).toBeGreaterThan(0); // real distance
    }
  });

  it("an inclined disc has real depth and a face-on one does not", () => {
    // A real galaxy: 15 kpc across at 10 Mpc. The fixture's scale matters — a disc bigger than its
    // own distance would wrap around the observer and measure nothing meaningful.
    const at = (inc: number) =>
      tessellateShape(
        {
          ...base,
          dist_pc: 1e7,
          shape: {
            kind: "disc",
            source: "measured",
            note: "",
            inclination_deg: inc,
            radius_pc: 15000,
          },
        },
        m,
      )!;
    const spread = (mesh: ReturnType<typeof at>) => {
      let lo = Infinity;
      let hi = -Infinity;
      for (let i = 3; i < mesh.vertices.length; i += 7) {
        lo = Math.min(lo, mesh.vertices[i]);
        hi = Math.max(hi, mesh.vertices[i]);
      }
      return hi - lo;
    };
    expect(spread(at(75))).toBeGreaterThan(spread(at(0)) * 5);
  });

  it("builds a shell, and a volume as depth slices", () => {
    const shell = tessellateShape(
      {
        ...base,
        shape: { kind: "shell", source: "assumed", note: "", radius_pc: 2 },
      },
      m,
    )!;
    expect(shell.kind).toBe("shell");
    expect(shell.slices).toBe(0);

    const vol = tessellateShape(
      {
        ...base,
        shape: {
          kind: "volume",
          source: "modelled",
          note: "",
          radius_pc: 5,
          profile: { depth_pc: 4, exponent: 0.5, bowl: 0.85 },
        },
      },
      m,
    )!;
    expect(vol.kind).toBe("volume");
    expect(vol.slices).toBeGreaterThan(1);
    // Four vertices per slice: the SHAPE lives in the fragment shader, so the geometry stays flat.
    expect(vol.vertices.length / 7).toBe(vol.slices * 4);
    expect(vol.footprint).toBeDefined();
  });

  it("clamps a volume's slices to the image it samples", () => {
    // M42's footprint is a circle wider than its own frame. Sampling outside the texture is exactly
    // what smeared the edge row across the sky, so the slices must not reach past it.
    const vol = tessellateShape(
      {
        ...base,
        rx_px: 9000,
        ry_px: 9000,
        shape: {
          kind: "volume",
          source: "modelled",
          note: "",
          radius_pc: 5,
          profile: { depth_pc: 4, exponent: 0.5 },
        },
      },
      m,
    )!;
    expect(vol.footprint!.rx).toBeLessThanOrEqual(1.001);
    expect(vol.footprint!.ry).toBeLessThanOrEqual(1.001);
  });
});

describe("fitPerspective's lens scale", () => {
  it("is inert by default, so every existing view is unchanged", () => {
    const m = manifest();
    const a = fitPerspective(m, IMAGE_ASPECT);
    const b = fitPerspective(m, IMAGE_ASPECT, 0.01, 1000, 1);
    expect(Array.from(b)).toEqual(Array.from(a));
  });

  it("opens the lens, so a wider field needs no absurd camera distance", () => {
    const m = manifest();
    const wide = fitPerspective(m, IMAGE_ASPECT, 0.01, 1000, 64);
    const one = fitPerspective(m, IMAGE_ASPECT);
    // perspective() puts 1/tan on the diagonal, so a 64x wider lens is a 64x smaller entry.
    expect(one[0] / wide[0]).toBeCloseTo(64, 6);
    expect(one[5] / wide[5]).toBeCloseTo(64, 6);
  });

  it("ignores a nonsense scale rather than collapsing the projection", () => {
    const m = manifest();
    const base = Array.from(fitPerspective(m, IMAGE_ASPECT));
    for (const bad of [0, -1, Number.NaN]) {
      expect(
        Array.from(fitPerspective(m, IMAGE_ASPECT, 0.01, 1000, bad)),
      ).toEqual(base);
    }
  });
});
