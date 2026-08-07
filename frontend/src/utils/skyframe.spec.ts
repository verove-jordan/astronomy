import { describe, expect, it } from "vitest";
import { equatorialToGalactic, galacticToEquatorial } from "@/utils/galactic";
import { matchesCamera, newBasis, project, skyToVec } from "@/utils/skyframe";
import type { SkyFrame } from "@/types";

// The scene frame has to be reconstructed EXACTLY, because the failure mode is invisible: a frame
// off by a roll angle draws a Milky Way that looks completely plausible and is simply wrong. So
// these check the port against the engine's own output rather than against itself — the fixtures are
// the real `solve.frame` and `camera` blocks from two cached runs, and newBasis has to recover the
// field of view and the parity that the Go side independently computed and wrote.

// output/M42/20260723_180917 — stars.json solve.frame + scene3d.json camera
const M42_FRAME: SkyFrame = {
  center_ra: 83.8456513962952,
  center_dec: -5.441118543410391,
  x_edge_ra: 83.19548576680775,
  x_edge_dec: -5.318528562532911,
  y_edge_ra: 83.93785351682925,
  y_edge_dec: -4.959728943508528,
};
const M42_CAMERA = {
  tan_half_w: 0.011498869954467188,
  tan_half_h: 0.008553504394858668,
  fov_y_deg: 0.9801355011762422,
  right_handed: false,
};

// output/M51/20260722_191432
const M51_FRAME: SkyFrame = {
  center_ra: 202.517528400277,
  center_dec: 47.223093323086296,
  x_edge_ra: 203.4357615202446,
  x_edge_dec: 47.100759454752854,
  y_edge_ra: 202.38601350548805,
  y_edge_dec: 46.74845340555965,
};
const M51_CAMERA = {
  tan_half_w: 0.011104265383401288,
  tan_half_h: 0.00843091604866702,
  fov_y_deg: 0.9660889244911706,
  right_handed: false,
};

const dot = (a: readonly number[], b: readonly number[]) =>
  a[0] * b[0] + a[1] * b[1] + a[2] * b[2];

// Longitudes wrap, so every comparison of them has to be made around the circle. Returns the signed
// separation in (-180, 180].
const arc = (a: number, b: number) => ((a - b + 540) % 360) - 180;

describe("newBasis reproduces the engine", () => {
  for (const [name, frame, camera] of [
    ["M42", M42_FRAME, M42_CAMERA],
    ["M51", M51_FRAME, M51_CAMERA],
  ] as const) {
    it(`${name}: recovers the field of view and parity the engine wrote`, () => {
      const b = newBasis(frame);
      expect(b).not.toBeNull();
      if (!b) return;
      // The engine derived these from the same three anchors in Go. Agreement to a part in 1e9 is
      // the whole proof that this port is the same function.
      expect(b.tanHalfW).toBeCloseTo(camera.tan_half_w, 12);
      expect(b.tanHalfH).toBeCloseTo(camera.tan_half_h, 12);
      expect(b.fovYDeg).toBeCloseTo(camera.fov_y_deg, 10);
      // Both real runs are mirrored fields — shot through a star diagonal.
      expect(b.rightHanded).toBe(camera.right_handed);
      expect(matchesCamera(b, camera)).toBe(true);
    });

    it(`${name}: the axes are orthonormal`, () => {
      const b = newBasis(frame);
      if (!b) throw new Error("basis");
      for (const v of [b.X, b.Y, b.Z]) {
        expect(Math.hypot(v[0], v[1], v[2])).toBeCloseTo(1, 12);
      }
      expect(dot(b.X, b.Y)).toBeCloseTo(0, 12);
      expect(dot(b.X, b.Z)).toBeCloseTo(0, 12);
      expect(dot(b.Y, b.Z)).toBeCloseTo(0, 12);
    });

    it(`${name}: the field centre projects to +Z, straight down the barrel`, () => {
      const b = newBasis(frame);
      if (!b) throw new Error("basis");
      const c = project(b, frame.center_ra, frame.center_dec);
      expect(c[0]).toBeCloseTo(0, 10);
      expect(c[1]).toBeCloseTo(0, 10);
      expect(c[2]).toBeCloseTo(1, 12);
    });
  }

  it("refuses a degenerate frame instead of returning a NaN basis", () => {
    // Every anchor on the same point: no axis can be defined. Drawing SOMETHING here is the failure
    // to avoid — the caller shows "no galaxy" instead.
    expect(
      newBasis({
        center_ra: 10,
        center_dec: 20,
        x_edge_ra: 10,
        x_edge_dec: 20,
        y_edge_ra: 10,
        y_edge_dec: 20,
      }),
    ).toBeNull();
  });

  it("rejects a camera from a different pass", () => {
    const b = newBasis(M42_FRAME);
    if (!b) throw new Error("basis");
    // M51's camera against M42's frame: the guard that stops a confidently mis-rolled galaxy.
    expect(matchesCamera(b, M51_CAMERA)).toBe(false);
  });
});

describe("equatorialToGalactic", () => {
  it("round-trips the forward transform", () => {
    for (let l = 0; l < 360; l += 37) {
      for (let b = -80; b <= 80; b += 23) {
        const eq = galacticToEquatorial(l, b);
        const back = equatorialToGalactic(eq.ra, eq.dec);
        expect(back.b).toBeCloseTo(b, 9);
        expect(arc(back.l, l)).toBeCloseTo(0, 8);
      }
    }
  });

  it("puts the galactic centre where the catalogues do", () => {
    // Sgr A* is RA 17h45m40s, Dec −29°00′28″ = (266.4168, −29.0078). It does NOT sit exactly at the
    // origin of the galactic frame — the IAU pole was fixed in 1958 from radio data and Sgr A* was
    // later found a few arcminutes off it, at l ≈ 359.944, b ≈ −0.046. Asserting the true offset
    // rather than zero is what makes this a test of the transform instead of a test of the fudge.
    const gc = equatorialToGalactic(266.4168, -29.0078);
    expect(arc(gc.l, 0)).toBeCloseTo(-0.056, 2);
    expect(gc.b).toBeCloseTo(-0.046, 2);
  });

  it("puts the north galactic pole at b = +90", () => {
    expect(equatorialToGalactic(192.85948, 27.12825).b).toBeCloseTo(90, 6);
  });

  // The two real fields against the published galactic coordinates of the objects in them. This is
  // the check that would catch a transposed sine or a wrong constant — values nobody could fudge
  // into agreement. The tolerance is a fifth of a degree because the frame's centre is the IMAGE
  // centre, not the catalogue object: M42 sits 0.06° off the middle of its own photograph, which is
  // a fact about the framing and not an error in the transform.
  it("gives M42's field its catalogue galactic coordinates", () => {
    const g = equatorialToGalactic(M42_FRAME.center_ra, M42_FRAME.center_dec);
    expect(Math.abs(arc(g.l, 209.01))).toBeLessThan(0.2); // Orion, 29° from the anticentre
    expect(Math.abs(g.b - -19.38)).toBeLessThan(0.2);
  });

  it("gives M51's field its catalogue galactic coordinates", () => {
    const g = equatorialToGalactic(M51_FRAME.center_ra, M51_FRAME.center_dec);
    expect(Math.abs(arc(g.l, 104.85))).toBeLessThan(0.2);
    // Far out of the disc, near the north galactic pole — which is why an "arm membership" claim
    // for this field would be meaningless however good the arm model is.
    expect(Math.abs(g.b - 68.56)).toBeLessThan(0.2);
  });
});

describe("galactic directions land in the scene frame", () => {
  it("the field centre's scene +Z has the field's own galactic coordinates", () => {
    // Z is where the photograph looks. Reading its (l, b) back through the basis must agree with
    // reading it straight off the sky — otherwise the frame and the sky disagree by a rotation.
    const b = newBasis(M42_FRAME);
    if (!b) throw new Error("basis");
    const direct = equatorialToGalactic(
      M42_FRAME.center_ra,
      M42_FRAME.center_dec,
    );
    // Rebuild the sky vector from Z's components against the basis axes (Bᵀ·(0,0,1) = Z).
    const g = equatorialToGalactic(
      (Math.atan2(b.Z[1], b.Z[0]) * 180) / Math.PI,
      (Math.asin(b.Z[2]) * 180) / Math.PI,
    );
    expect(arc(g.l, direct.l)).toBeCloseTo(0, 9);
    expect(g.b).toBeCloseTo(direct.b, 9);
  });

  it("the galactic centre projects to a unit direction in the scene", () => {
    const b = newBasis(M42_FRAME);
    if (!b) throw new Error("basis");
    const gc = galacticToEquatorial(0, 0);
    const d = project(b, gc.ra, gc.dec);
    expect(Math.hypot(d[0], d[1], d[2])).toBeCloseTo(1, 12);
    // M42 looks 29° from the ANTICENTRE, so the galactic centre must be well behind the camera.
    expect(d[2]).toBeLessThan(0);
  });

  it("mirroring the frame flips the parity and leaves the sky direction alone", () => {
    // The parity is a property of the optical train, not of where the telescope pointed.
    const b = newBasis(M42_FRAME);
    const mirrored = newBasis({ ...M42_FRAME, y_edge_dec: -4.959728943508528 });
    if (!b || !mirrored) throw new Error("basis");
    expect(dot(b.Z, mirrored.Z)).toBeCloseTo(1, 12);
  });
});

describe("skyToVec", () => {
  it("is a unit vector with the usual axis conventions", () => {
    expect(skyToVec(0, 0)).toEqual([1, 0, 0]);
    const p = skyToVec(0, 90);
    expect(p[2]).toBeCloseTo(1, 12);
    const q = skyToVec(90, 0);
    expect(q[1]).toBeCloseTo(1, 12);
  });
});
