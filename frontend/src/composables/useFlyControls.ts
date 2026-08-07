import { onBeforeUnmount, onMounted, ref, watch, type Ref } from "vue";
import {
  LOOK_GAIN_DRAG,
  LOOK_GAIN_LOCKED,
  dragToLook,
  fly,
  flySteps,
  look,
  lookPerPixel,
  type Move,
} from "@/utils/scene3dfly";
import type { Orbit } from "@/utils/scene3d";

// Exploration mode: the keyboard flies, the mouse looks.
//
// Kept out of useStarField3D because it shares nothing with it but the orbit — no GL, no picking, no
// buffers — and because a movement loop is the kind of thing that should be readable on its own.
//
// Two decisions worth knowing before changing anything here:
//
//   - Keys are integrated on a CLOCK, never on the key event. Key repeat fires at whatever rate the
//     operating system feels like and stops entirely when a second key goes down, so moving a camera
//     from keydown gives a speed that depends on the typist and stutters on every chord. Holding a
//     key sets a flag; a rAF loop turns flags plus elapsed time into distance.
//   - The loop only runs while something is held. An idle exploration mode costs nothing, which
//     matters because the renderer deliberately has no idle loop of its own.

/** FlyKey is the set of movement inputs, tracked per direction rather than per physical key. */
type FlyKey = "forward" | "back" | "left" | "right" | "up" | "down";

// Arrows for people who have never played a game, WASD for people who have, and both at once for
// nobody's surprise. Space/C lift and drop; shift is the boost.
const KEY_MAP: Record<string, FlyKey> = {
  ArrowUp: "forward",
  ArrowDown: "back",
  ArrowLeft: "left",
  ArrowRight: "right",
  w: "forward",
  s: "back",
  a: "left",
  d: "right",
  z: "forward", // AZERTY, where W and Z swap
  q: "left", // AZERTY, where A and Q swap
  " ": "up",
  c: "down",
};

export interface FlyControls {
  /** flying is whether exploration mode is on. */
  flying: Ref<boolean>;
  /** pointerLocked is whether the cursor is currently captured for free-look. */
  pointerLocked: Ref<boolean>;
  /** moving is whether any movement key is down — for the on-screen indicator. */
  moving: Ref<boolean>;
  toggle(): void;
  enable(): void;
  disable(): void;
  /** togglePointerLock captures the cursor, or releases it if already captured. */
  togglePointerLock(): void;
  /** applyLook turns the camera by a mouse delta in pixels. */
  applyLook(dxPx: number, dyPx: number): void;
}

export function useFlyControls(input: {
  orbit: Ref<Orbit>;
  requestDraw: () => void;
  /** canvas is what gets the pointer lock and the keyboard focus. */
  canvas: Ref<HTMLElement | null>;
  /**
   * tanHalfH is the camera's half-height tangent — how wide the view is.
   *
   * Sideways movement is scaled by it because the field is a needle: thirty times longer than it is
   * wide. Without it, one speed serves both axes and strafing leaves the scene instantly.
   */
  tanHalfH: Ref<number>;
}): FlyControls {
  const flying = ref(false);
  const pointerLocked = ref(false);
  // hovering is what arms the keyboard. Without it the arrows would belong to this view for the
  // whole page — a 3D field halfway down a long page would silently eat the scroll keys.
  const hovering = ref(false);
  const held = new Set<FlyKey>();
  const boosted = ref(false);
  // moving drives the on-screen indicator, so it follows the held set rather than the loop.
  const moving = ref(false);

  let raf = 0;
  let lastAt = 0;

  function directionOf(e: KeyboardEvent): FlyKey | undefined {
    // Lower-cased so a shifted letter (boost + W) still moves, and never matched when a modifier
    // that means something else to the browser is down.
    if (e.ctrlKey || e.metaKey || e.altKey) return undefined;
    return KEY_MAP[e.key.length === 1 ? e.key.toLowerCase() : e.key];
  }

  // armed is whether this view owns the keyboard right now: the pointer is over it, it has been
  // clicked, or it has captured the cursor outright.
  function armed() {
    return (
      pointerLocked.value ||
      hovering.value ||
      (!!input.canvas.value && document.activeElement === input.canvas.value)
    );
  }

  function onKeyDown(e: KeyboardEvent) {
    // Armed even while already flying, so moving the pointer away and clicking elsewhere hands the
    // arrow keys back to the page. Focus counts, which is why entering the mode focuses the canvas:
    // it keeps the keys working when the pointer wanders off, and a click anywhere else ends that.
    if (!armed()) return;
    if (e.key === "Escape") {
      // One key that always gets you out, whatever state the mode is in.
      disable();
      return;
    }
    if (e.key === "Shift") boosted.value = true;
    const dir = directionOf(e);
    if (!dir) return;
    // Pressing an arrow over the view IS the request to fly. Requiring the button first is what
    // made this feature look broken: the keys did nothing, and nothing said why. The button stays —
    // it is how the mode is discovered and how the cursor gets captured — but it is no longer the
    // only door.
    if (!flying.value) enable();
    // Space scrolls the page and the arrows scroll it under the canvas; neither is wanted while the
    // same keys are flying a camera.
    e.preventDefault();
    if (!held.has(dir)) {
      held.add(dir);
      start();
    }
  }

  function onKeyUp(e: KeyboardEvent) {
    if (e.key === "Shift") boosted.value = false;
    const dir = directionOf(e);
    if (dir) held.delete(dir);
    if (!held.size) moving.value = false;
  }

  // A window that loses focus never delivers the keyup, which would leave the camera flying off on
  // its own — the same class of bug as a jog with no pointerup.
  function onBlur() {
    held.clear();
    boosted.value = false;
    moving.value = false;
  }

  function moveVector(): Move {
    return {
      forward: (held.has("forward") ? 1 : 0) - (held.has("back") ? 1 : 0),
      right: (held.has("right") ? 1 : 0) - (held.has("left") ? 1 : 0),
      up: (held.has("up") ? 1 : 0) - (held.has("down") ? 1 : 0),
    };
  }

  function start() {
    if (raf) return;
    lastAt = performance.now();
    moving.value = true;
    raf = requestAnimationFrame(tick);
  }

  function tick(now: number) {
    raf = 0;
    if (!flying.value || !held.size) {
      moving.value = false;
      return;
    }
    const dt = now - lastAt;
    lastAt = now;
    input.orbit.value = fly(
      input.orbit.value,
      moveVector(),
      flySteps(input.orbit.value, input.tanHalfH.value, dt, boosted.value),
    );
    input.requestDraw();
    raf = requestAnimationFrame(tick);
  }

  function applyLook(dxPx: number, dyPx: number) {
    // Measured against the live canvas rather than a stored size: fullscreen changes the viewport
    // without changing anything else, and a sensitivity that ignored it would double on the way in.
    const perPx = lookPerPixel(
      input.tanHalfH.value,
      input.canvas.value?.clientHeight ?? 0,
      pointerLocked.value ? LOOK_GAIN_LOCKED : LOOK_GAIN_DRAG,
    );
    const { dYaw, dPitch } = dragToLook(dxPx, dyPx, perPx);
    input.orbit.value = look(input.orbit.value, dYaw, dPitch);
    input.requestDraw();
  }

  function onPointerLockChange() {
    pointerLocked.value = document.pointerLockElement === input.canvas.value;
    // Leaving the lock (Escape, or the browser deciding) must not leave a key stuck down.
    if (!pointerLocked.value) onBlur();
  }

  function onLockedMouseMove(e: MouseEvent) {
    if (!pointerLocked.value) return;
    applyLook(e.movementX, e.movementY);
  }

  function togglePointerLock() {
    const el = input.canvas.value;
    if (!el || !flying.value) return;
    if (pointerLocked.value) {
      document.exitPointerLock?.();
      return;
    }
    // requestPointerLock rejects outside a user gesture and on some browsers returns a promise that
    // rejects rather than throwing; neither is worth surfacing — the hold-to-look path still works,
    // so a refused lock degrades to the default rather than to nothing.
    void Promise.resolve(el.requestPointerLock?.()).catch(() => {});
  }

  function enable() {
    if (flying.value) return;
    flying.value = true;
    // Focusing the canvas keeps the keys coming once the pointer wanders off the view.
    input.canvas.value?.focus?.();
  }

  function disable() {
    if (!flying.value) return;
    flying.value = false;
    onBlur();
    if (raf) cancelAnimationFrame(raf);
    raf = 0;
    if (document.pointerLockElement === input.canvas.value) {
      document.exitPointerLock?.();
    }
    pointerLocked.value = false;
  }

  function onEnter() {
    hovering.value = true;
  }

  function onLeave() {
    hovering.value = false;
  }

  // The key listeners live for as long as the component does, rather than being added when the mode
  // turns on. They have to: the press that STARTS the mode has to be heard by something, and a
  // listener installed by that same press arrives too late for it.
  onMounted(() => {
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", onBlur);
    document.addEventListener("pointerlockchange", onPointerLockChange);
    document.addEventListener("mousemove", onLockedMouseMove);
  });

  // The canvas arrives after setup, and can be swapped, so hover is bound by watching the ref.
  watch(
    input.canvas,
    (el, old) => {
      old?.removeEventListener("pointerenter", onEnter);
      old?.removeEventListener("pointerleave", onLeave);
      el?.addEventListener("pointerenter", onEnter);
      el?.addEventListener("pointerleave", onLeave);
    },
    { immediate: true },
  );

  function toggle() {
    if (flying.value) disable();
    else enable();
  }

  onBeforeUnmount(() => {
    disable();
    window.removeEventListener("keydown", onKeyDown);
    window.removeEventListener("keyup", onKeyUp);
    window.removeEventListener("blur", onBlur);
    document.removeEventListener("pointerlockchange", onPointerLockChange);
    document.removeEventListener("mousemove", onLockedMouseMove);
    input.canvas.value?.removeEventListener("pointerenter", onEnter);
    input.canvas.value?.removeEventListener("pointerleave", onLeave);
  });

  return {
    flying,
    pointerLocked,
    moving,
    toggle,
    enable,
    disable,
    togglePointerLock,
    applyLook,
  };
}
