import { describe, expect, it } from "vitest";
import {
  PITCH_LIMIT,
  defaultOrbit,
  eyePosition,
  fitPerspective,
  multiply,
  panOrbit,
  projectToScreen,
  viewMatrix,
  type Orbit,
} from "@/utils/scene3d";
import {
  LOOK_GAIN_DRAG,
  cameraBasis,
  dolly,
  dragToLook,
  fly,
  flySteps,
  look,
  lookPerPixel,
  orbitPerPixel,
} from "@/utils/scene3dfly";

// A representative astrograph: a couple of degrees across, which is what makes the depth cone a
// needle and forces the two axes apart.
const TAN_H = 0.0175;
const steps = (f: number, l = f) => ({ forward: f, lateral: l });

// Every sign in the fly maths is a thing that looks fine and is backwards: scene Y points down, and
// a camera that strafes the wrong way or climbs when told to descend is indistinguishable from one
// that is simply hard to drive. These pin the directions against the orbit's own eye position, which
// is the same function the renderer projects with.

const near = (a: number, b: number, tol = 1e-9) => Math.abs(a - b) <= tol;

describe("cameraBasis", () => {
  it("is orthonormal", () => {
    const o = { ...defaultOrbit(), yaw: 0.7, pitch: -0.3 };
    const { forward, right, up } = cameraBasis(o);
    for (const v of [forward, right, up]) {
      expect(near(Math.hypot(v[0], v[1], v[2]), 1, 1e-6)).toBe(true);
    }
    const dot = (a: number[], b: number[]) =>
      a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
    expect(near(dot(forward, right), 0, 1e-6)).toBe(true);
    expect(near(dot(forward, up), 0, 1e-6)).toBe(true);
    expect(near(dot(right, up), 0, 1e-6)).toBe(true);
  });

  it("points forward at what the camera is looking at", () => {
    const o = { ...defaultOrbit(), yaw: 0.4, pitch: 0.2 };
    const eye = eyePosition(o);
    const { forward } = cameraBasis(o);
    // Stepping from the eye along forward must approach the target, never retreat from it.
    const before = Math.hypot(
      o.target[0] - eye[0],
      o.target[1] - eye[1],
      o.target[2] - eye[2],
    );
    const after = Math.hypot(
      o.target[0] - (eye[0] + forward[0] * 0.1),
      o.target[1] - (eye[1] + forward[1] * 0.1),
      o.target[2] - (eye[2] + forward[2] * 0.1),
    );
    expect(after).toBeLessThan(before);
  });

  it("has up pointing along −Y at zero roll, because scene Y points down", () => {
    const o = { ...defaultOrbit(), yaw: 0, pitch: 0, roll: 0 };
    expect(cameraBasis(o).up[1]).toBeLessThan(0);
  });
});

describe("fly", () => {
  it("moves the eye and the target by the same vector, leaving the view direction alone", () => {
    const o = { ...defaultOrbit(), yaw: 0.3, pitch: 0.1 };
    const before = eyePosition(o);
    const moved = fly(o, { forward: 1, right: 0, up: 0 }, steps(0.25));
    const after = eyePosition(moved);
    for (let i = 0; i < 3; i++) {
      expect(
        near(after[i] - before[i], moved.target[i] - o.target[i], 1e-9),
      ).toBe(true);
    }
    // Orientation and distance are untouched — flying is translation, never rotation.
    expect(moved.yaw).toBe(o.yaw);
    expect(moved.pitch).toBe(o.pitch);
    expect(moved.distance).toBe(o.distance);
  });

  it("flies forward towards where the camera looks", () => {
    const o = { ...defaultOrbit(), yaw: 0.8, pitch: -0.2 };
    const eye = eyePosition(o);
    const { forward } = cameraBasis(o);
    const moved = fly(o, { forward: 1, right: 0, up: 0 }, steps(0.3));
    const eye2 = eyePosition(moved);
    for (let i = 0; i < 3; i++) {
      expect(near(eye2[i] - eye[i], forward[i] * 0.3, 1e-6)).toBe(true);
    }
  });

  it("takes 'up' towards the top of the world, not the top of the screen", () => {
    // Pitched hard over, camera-up and world-up disagree; the control must still climb.
    const o = { ...defaultOrbit(), pitch: 1.2 };
    const moved = fly(o, { forward: 0, right: 0, up: 1 }, steps(0.5));
    // Scene Y points down, so climbing DECREASES y.
    expect(moved.target[1]).toBeLessThan(o.target[1]);
    expect(near(moved.target[0], o.target[0])).toBe(true);
    expect(near(moved.target[2], o.target[2])).toBe(true);
  });

  it("does not reward diagonals with extra speed", () => {
    const o = defaultOrbit();
    const straight = fly(o, { forward: 1, right: 0, up: 0 }, steps(0.4));
    const diagonal = fly(o, { forward: 1, right: 1, up: 0 }, steps(0.4));
    const travelled = (x: typeof o) =>
      Math.hypot(
        x.target[0] - o.target[0],
        x.target[1] - o.target[1],
        x.target[2] - o.target[2],
      );
    expect(near(travelled(straight), travelled(diagonal), 1e-9)).toBe(true);
  });

  it("stands still for no input", () => {
    const o = defaultOrbit();
    expect(fly(o, { forward: 0, right: 0, up: 0 }, steps(0.5))).toEqual(o);
  });
});

describe("flySteps", () => {
  it("scales with viewing distance so the control feels the same at every zoom", () => {
    const nearOrbit = { ...defaultOrbit(), distance: 0.1 };
    const farOrbit = { ...defaultOrbit(), distance: 10 };
    const ratio =
      flySteps(farOrbit, TAN_H, 16).forward /
      flySteps(nearOrbit, TAN_H, 16).forward;
    expect(ratio).toBeCloseTo(100, 6);
  });

  // The regression this whole split exists for: sharing the radial speed sideways sent the camera
  // out of a two-degree field in about a twentieth of a second.
  it("moves sideways far slower than forward in a narrow field", () => {
    const o = { ...defaultOrbit(), distance: 5 };
    const { forward, lateral } = flySteps(o, TAN_H, 16);
    expect(lateral).toBeLessThan(forward / 20);
  });

  it("crosses a fixed fraction of the SCREEN sideways, whatever the focal length", () => {
    // Two very different fields: the lateral step must stay the same share of what is on screen.
    const o = { ...defaultOrbit(), distance: 3 };
    const share = (tan: number) =>
      flySteps(o, tan, 16).lateral / (2 * tan * o.distance);
    expect(share(0.0175)).toBeCloseTo(share(0.4), 9);
  });

  it("falls back to a usable width rather than a dead axis when the field is unknown", () => {
    const o = defaultOrbit();
    for (const bad of [0, -1, Number.NaN]) {
      expect(flySteps(o, bad, 16).lateral).toBeGreaterThan(0);
    }
  });

  it("boosts both axes", () => {
    const o = defaultOrbit();
    const plain = flySteps(o, TAN_H, 16, false);
    const fast = flySteps(o, TAN_H, 16, true);
    expect(fast.forward).toBeCloseTo(plain.forward * 3, 9);
    expect(fast.lateral).toBeCloseTo(plain.lateral * 3, 9);
  });

  it("clamps a long frame so a backgrounded tab cannot teleport the camera", () => {
    const o = defaultOrbit();
    // Ten seconds of stalled rAF must cost no more than the 100 ms cap.
    expect(flySteps(o, TAN_H, 10_000).forward).toBeCloseTo(
      flySteps(o, TAN_H, 100).forward,
      9,
    );
  });
});

describe("look", () => {
  it("clamps pitch short of the poles, where the up vector flips", () => {
    const o = defaultOrbit();
    expect(look(o, 0, 99).pitch).toBeCloseTo(PITCH_LIMIT, 9);
    expect(look(o, 0, -99).pitch).toBeCloseTo(-PITCH_LIMIT, 9);
  });

  // The whole point of exploration mode: the head turns, the body stays put. Changing yaw and pitch
  // alone would swing the EYE around the target instead, which is the lurch that makes flying
  // through a volume unusable.
  it("leaves the eye exactly where it was", () => {
    const o = { ...defaultOrbit(), yaw: 0.2, pitch: -0.1, distance: 2.5 };
    const before = eyePosition(o);
    const after = eyePosition(look(o, 0.4, 0.2));
    for (let i = 0; i < 3; i++)
      expect(near(after[i], before[i], 1e-9)).toBe(true);
  });

  it("actually changes where the camera looks", () => {
    const o = { ...defaultOrbit(), yaw: 0.2 };
    const turned = look(o, 0.3, 0);
    expect(turned.target).not.toEqual(o.target);
    expect(cameraBasis(turned).forward).not.toEqual(cameraBasis(o).forward);
    expect(turned.distance).toBe(o.distance);
  });

  it("keeps the eye put even when pitch is clamped at the pole", () => {
    // The clamp changes the angle by less than asked; the target correction must follow the clamped
    // value, not the requested one, or the camera slides every time you look too far up.
    const o = { ...defaultOrbit(), pitch: PITCH_LIMIT - 0.001 };
    const before = eyePosition(o);
    const after = eyePosition(look(o, 0, 5));
    for (let i = 0; i < 3; i++)
      expect(near(after[i], before[i], 1e-9)).toBe(true);
  });
});

describe("dolly", () => {
  it("keeps the distance inside the range the orbit zoom accepts", () => {
    const o = defaultOrbit();
    expect(dolly(o, 1e9).distance).toBeLessThanOrEqual(400);
    expect(dolly(o, 1e-9).distance).toBeGreaterThanOrEqual(0.004);
  });

  it("ignores a nonsense factor rather than producing a camera with no position", () => {
    const o = defaultOrbit();
    expect(dolly(o, 0).distance).toBe(o.distance);
    expect(dolly(o, -1).distance).toBe(o.distance);
  });
});

describe("lookPerPixel", () => {
  const HEIGHT = 900;

  // The regression: a flat 0.005 rad/px swept a two-degree field in about seven pixels of travel.
  it("turns by one pixel's own angular size, so a drag is one-to-one", () => {
    const perPx = lookPerPixel(TAN_H, HEIGHT, LOOK_GAIN_DRAG);
    // Dragging the full height of the viewport must sweep exactly one field, not a hundred.
    const sweptFields = (perPx * HEIGHT) / (2 * Math.atan(TAN_H));
    expect(sweptFields).toBeCloseTo(1, 2);
  });

  it("is over a hundred times gentler than the old constant in a narrow field", () => {
    expect(lookPerPixel(TAN_H, HEIGHT, LOOK_GAIN_DRAG)).toBeLessThan(
      0.005 / 100,
    );
  });

  it("feels the same at every focal length", () => {
    // Wide and narrow fields must both sweep one screen per screen-height of drag.
    const swept = (tan: number) =>
      (lookPerPixel(tan, HEIGHT, LOOK_GAIN_DRAG) * HEIGHT) / (2 * tan);
    expect(swept(0.0175)).toBeCloseTo(swept(0.6), 9);
  });

  it("compensates for viewport height, so going fullscreen does not change the feel", () => {
    const small = lookPerPixel(TAN_H, 400, LOOK_GAIN_DRAG);
    const large = lookPerPixel(TAN_H, 1600, LOOK_GAIN_DRAG);
    // Half the pixels, twice the turn each — the same total sweep for the same fraction of a drag.
    expect(small * 400).toBeCloseTo(large * 1600, 9);
  });

  it("stays usable when the field or the viewport is unknown", () => {
    for (const [tan, h] of [
      [0, 900],
      [Number.NaN, 900],
      [TAN_H, 0],
      [-1, -1],
    ] as [number, number][]) {
      const v = lookPerPixel(tan, h, LOOK_GAIN_DRAG);
      expect(Number.isFinite(v)).toBe(true);
      expect(v).toBeGreaterThan(0);
    }
  });
});

describe("dragToLook", () => {
  const dot = (a: readonly number[], b: readonly number[]) =>
    a[0] * b[0] + a[1] * b[1] + a[2] * b[2];

  // Both of these say the same thing: the field follows the pointer. Asserted against the camera's
  // own axes rather than against the sign of yaw or pitch, so the conventions of the orbit rig can
  // change underneath without quietly inverting an axis.
  it("follows the pointer horizontally — dragging right swings the view left", () => {
    const o = { ...defaultOrbit(), yaw: 0.3, pitch: 0.1 };
    const before = cameraBasis(o);
    const { dYaw, dPitch } = dragToLook(120, 0, 1e-3);
    const after = cameraBasis(look(o, dYaw, dPitch));
    // Turning left means the new view direction leans against the old RIGHT axis, which is what
    // carries the stars rightwards with the hand.
    expect(dot(after.forward, before.right)).toBeLessThan(0);
  });

  it("follows the pointer vertically — dragging down swings the view up", () => {
    const o = { ...defaultOrbit(), yaw: 0.3, pitch: 0.1 };
    const before = cameraBasis(o);
    const { dYaw, dPitch } = dragToLook(0, 120, 1e-3);
    const after = cameraBasis(look(o, dYaw, dPitch));
    expect(dot(after.forward, before.up)).toBeGreaterThan(0);
  });

  it("agrees on both axes at once, so a diagonal drag does not fight itself", () => {
    const o = { ...defaultOrbit(), yaw: -0.4, pitch: -0.2 };
    const before = cameraBasis(o);
    const { dYaw, dPitch } = dragToLook(120, 120, 1e-3);
    const after = cameraBasis(look(o, dYaw, dPitch));
    expect(dot(after.forward, before.right)).toBeLessThan(0);
    expect(dot(after.forward, before.up)).toBeGreaterThan(0);
  });

  it("stands still for no movement", () => {
    expect(dragToLook(0, 0, 1e-3)).toEqual({ dYaw: 0, dPitch: -0 });
  });
});

describe("orbitPerPixel", () => {
  // Orbiting is measured in fractions of a turn, not pixels of sky: you use it to get round to the
  // other side of the field, and at the look rate that would take a hundred and eighty drags.
  it("makes a full-height drag half a revolution", () => {
    for (const h of [400, 900, 1600]) {
      expect(orbitPerPixel(h) * h).toBeCloseTo(Math.PI, 12);
    }
  });

  it("is far brisker than looking around in a narrow field", () => {
    expect(orbitPerPixel(900)).toBeGreaterThan(lookPerPixel(0.0175, 900) * 50);
  });

  it("stays usable for a degenerate viewport", () => {
    for (const h of [0, -1, Number.NaN]) {
      expect(orbitPerPixel(h)).toBeGreaterThan(0);
    }
  });
});

describe("panOrbit direction", () => {
  const dot = (a: readonly number[], b: readonly number[]) =>
    a[0] * b[0] + a[1] * b[1] + a[2] * b[2];

  // Panning translates the whole rig, so a fixed world point shifts the opposite way in camera axes.
  // Screen position is therefore its offset from the eye along right (screen-right) and up
  // (screen-UP — so a point moving DOWN the screen has a DECREASING up coordinate).
  function screenOf(o: Orbit, P: [number, number, number]) {
    const eye = eyePosition(o);
    const { right, up } = cameraBasis(o);
    const rel: [number, number, number] = [
      P[0] - eye[0],
      P[1] - eye[1],
      P[2] - eye[2],
    ];
    return { x: dot(rel, right), y: dot(rel, up) };
  }

  it("carries the field with the pointer on BOTH axes", () => {
    const o: Orbit = { ...defaultOrbit(), yaw: 0.3, pitch: 0.15, distance: 2 };
    const P: [number, number, number] = [0.4, -0.2, 2.5];
    const before = screenOf(o, P);
    // Drag right: the field must travel right.
    expect(screenOf(panOrbit(o, 40, 0, 900, 0.02), P).x).toBeGreaterThan(
      before.x,
    );
    // Drag down: the field must travel down, i.e. its screen-UP coordinate falls. This is the one
    // that was backwards — identical signs on the two terms drag x correctly and y inverted.
    expect(screenOf(panOrbit(o, 0, 40, 900, 0.02), P).y).toBeLessThan(before.y);
  });

  it("agrees with looking around, so switching gesture never reverses the field", () => {
    const o: Orbit = { ...defaultOrbit(), yaw: -0.2, pitch: 0.1, distance: 2 };
    const P: [number, number, number] = [0.1, 0.3, 2.2];
    const panned = screenOf(panOrbit(o, 30, 30, 900, 0.02), P);
    const before = screenOf(o, P);
    const { dYaw, dPitch } = dragToLook(30, 30, 1e-3);
    const looked = cameraBasis(look(o, dYaw, dPitch));
    // Pan moves it right and down; look turns so the field goes right and down too.
    expect(panned.x).toBeGreaterThan(before.x);
    expect(panned.y).toBeLessThan(before.y);
    expect(dot(looked.forward, cameraBasis(o).right)).toBeLessThan(0);
    expect(dot(looked.forward, cameraBasis(o).up)).toBeGreaterThan(0);
  });
});

// The end-to-end direction test: where does a star actually LAND on the canvas?
//
// Every previous attempt at these signs reasoned about camera axes and got one of them backwards
// anyway, three rounds running. This measures instead — it pushes a point through the very matrices
// the renderer draws with (viewMatrix ∘ fitPerspective ∘ projectToScreen) and reads the pixel. If a
// gesture ever pushes the field the wrong way again, this fails, whatever the algebra says.
describe("the field follows the pointer (measured in canvas pixels)", () => {
  const VP = { width: 1200, height: 900 };
  // A manifest shaped like a real astrograph run (M42: ~1° field, 4656×3520).
  const TAN_HALF_H = 0.00855;
  const MANIFEST = {
    image: { width: 4656, height: 3520 },
    camera: { tan_half_w: 0.0115, tan_half_h: TAN_HALF_H },
  } as unknown as Parameters<typeof fitPerspective>[0];

  function pixelOf(o: Orbit, p: [number, number, number]) {
    const vp = multiply(
      fitPerspective(MANIFEST, VP.width / VP.height),
      viewMatrix(o),
    );
    return projectToScreen(p, vp, VP);
  }

  // A star inside the cone, off-axis so a pure rotation actually moves it.
  const STAR: [number, number, number] = [0.004, -0.003, 1.6];
  const BASE: Orbit = { ...defaultOrbit(), distance: 1, roll: 0 };

  function moved(after: Orbit) {
    const a = pixelOf(BASE, STAR);
    const b = pixelOf(after, STAR);
    if (!a || !b) throw new Error("star projected behind the camera");
    return { dx: b[0] - a[0], dy: b[1] - a[1] };
  }

  it("LOOK: a drag carries the star the same way as the hand", () => {
    const perPx = lookPerPixel(TAN_HALF_H, VP.height);
    const right = dragToLook(60, 0, perPx);
    const down = dragToLook(0, 60, perPx);
    // Canvas y grows downward, so "the star follows a downward drag" is dy > 0.
    expect(moved(look(BASE, right.dYaw, right.dPitch)).dx).toBeGreaterThan(0);
    expect(moved(look(BASE, down.dYaw, down.dPitch)).dy).toBeGreaterThan(0);
  });

  it("ORBIT: a drag carries the star the same way as the hand", () => {
    const perPx = orbitPerPixel(VP.height);
    const spin = (dxPx: number, dyPx: number): Orbit => {
      const { dYaw, dPitch } = dragToLook(dxPx, dyPx, perPx);
      return {
        ...BASE,
        yaw: BASE.yaw + dYaw,
        pitch: Math.max(
          -PITCH_LIMIT,
          Math.min(PITCH_LIMIT, BASE.pitch + dPitch),
        ),
      };
    };
    expect(moved(spin(20, 0)).dx).toBeGreaterThan(0);
    expect(moved(spin(0, 20)).dy).toBeGreaterThan(0);
  });

  it("PAN: a drag carries the star the same way as the hand", () => {
    const th = TAN_HALF_H;
    expect(moved(panOrbit(BASE, 60, 0, VP.height, th)).dx).toBeGreaterThan(0);
    expect(moved(panOrbit(BASE, 0, 60, VP.height, th)).dy).toBeGreaterThan(0);
  });

  it("all three gestures agree, so switching one never reverses the field", () => {
    const lookP = dragToLook(40, 40, lookPerPixel(TAN_HALF_H, VP.height));
    const orbP = dragToLook(20, 20, orbitPerPixel(VP.height));
    const byLook = moved(look(BASE, lookP.dYaw, lookP.dPitch));
    const byOrbit = moved({
      ...BASE,
      yaw: BASE.yaw + orbP.dYaw,
      pitch: BASE.pitch + orbP.dPitch,
    });
    const byPan = moved(panOrbit(BASE, 40, 40, VP.height, TAN_HALF_H));
    for (const m of [byLook, byOrbit, byPan]) {
      expect(Math.sign(m.dx)).toBe(1);
      expect(Math.sign(m.dy)).toBe(1);
    }
  });
});
