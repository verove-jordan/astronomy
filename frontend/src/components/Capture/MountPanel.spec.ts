import { createTestingPinia } from "@pinia/testing";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

import MountPanel from "./MountPanel.vue";

// Keyboard jogging moves a telescope, so the two properties that matter are: a held key keeps the
// axis alive, and EVERY way of letting go stops it. The second is the one that breaks silently.

const posts: { path: string; body: Record<string, unknown> }[] = [];

vi.mock("@/services/api", () => ({
  apiGet: vi.fn(async () => ({ ports: [] })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => {
    posts.push({ path, body });
    return {};
  }),
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (k: string) => k }),
}));

function jogs() {
  return posts.filter((p) => p.path === "/api/device/mount/jog");
}

function starts() {
  return jogs().filter((p) => (p.body.rate as number) > 0);
}

function stops() {
  return jogs().filter((p) => p.body.rate === 0);
}

function mountPanel() {
  return mount(MountPanel, {
    global: {
      plugins: [
        createTestingPinia({
          createSpy: vi.fn,
          initialState: {
            capture: {
              mount: {
                connected: true,
                mount: { ra_deg: 10, dec_deg: 41, alt_deg: 80, aligned: false },
              },
            },
          },
        }),
      ],
    },
  });
}

describe("MountPanel keyboard jogging", () => {
  beforeEach(() => {
    posts.length = 0;
  });

  it("maps each arrow to the axis its button drives", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    for (const [key, direction] of [
      ["ArrowUp", "north"],
      ["ArrowDown", "south"],
      ["ArrowLeft", "east"],
      ["ArrowRight", "west"],
    ]) {
      posts.length = 0;
      await pad.trigger("keydown", { key });
      await pad.trigger("keyup", { key });
      expect(starts()[0]?.body.direction).toBe(direction);
    }
  });

  it("ignores auto-repeat rather than flooding a 9600-baud link", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowUp" });
    await pad.trigger("keydown", { key: "ArrowUp", repeat: true });
    await pad.trigger("keydown", { key: "ArrowUp", repeat: true });

    expect(starts()).toHaveLength(1);
  });

  it("stops the axis when the key is released", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowUp" });
    expect(stops()).toHaveLength(0);
    await pad.trigger("keyup", { key: "ArrowUp" });
    expect(stops().length).toBeGreaterThan(0);
  });

  it("stops the axis when focus is lost with the key still held", async () => {
    // A keyup that never arrives is the whole reason the server has a deadman. Stopping here as well
    // means the mount halts in milliseconds instead of in four seconds.
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowLeft" });
    await pad.trigger("blur");

    expect(stops().length).toBeGreaterThan(0);
  });

  // Two arrows on DIFFERENT axes drive both motors at once, the way the hand controller does. This
  // replaces an earlier rule that switched axis instead — one motor per axis means both can run,
  // and a pad that refused diagonals was a limitation of this panel, never of the mount.
  it("drives both axes at once when arrows on different axes are held", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowUp" });
    await pad.trigger("keydown", { key: "ArrowLeft" });

    // Neither axis was stopped to make room for the other.
    expect(stops()).toHaveLength(0);
    expect(starts().map((p) => p.body.direction)).toEqual(["north", "east"]);
  });

  it("reverses within an axis rather than running one motor both ways", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowLeft" });
    await pad.trigger("keydown", { key: "ArrowRight" });

    // The same motor cannot run east and west together, so east is stopped before west starts.
    expect(stops().map((p) => p.body.direction)).toEqual(["east"]);
    expect(starts().map((p) => p.body.direction)).toEqual(["east", "west"]);
  });

  it("releasing one arrow of a diagonal stops only that axis", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowUp" });
    await pad.trigger("keydown", { key: "ArrowLeft" });
    await pad.trigger("keyup", { key: "ArrowUp" });

    // Only the declination motor is halted; the right-ascension one keeps running.
    expect(stops().map((p) => p.body.direction)).toEqual(["north"]);
  });

  it("asks the server to stop the axis by itself if no renewal arrives", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "ArrowUp" });
    // hold_ms is what arms the deadman; without it a closed tab leaves the mount slewing.
    expect(starts()[0]?.body.hold_ms).toBeGreaterThan(0);
  });

  it("leaves other keys alone", async () => {
    const wrapper = mountPanel();
    const pad = wrapper.find('[role="group"]');

    await pad.trigger("keydown", { key: "a" });
    await pad.trigger("keydown", { key: "Tab" });

    expect(jogs()).toHaveLength(0);
  });
});
