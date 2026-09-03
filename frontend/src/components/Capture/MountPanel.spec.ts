import { createTestingPinia } from "@pinia/testing";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useCaptureStore } from "@/stores/capture";

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

// Reading the mount back, and the two properties that matter for a control that writes to hardware:
// it is never done behind the user's back, and the destructive half is a preview until it is not.
describe("MountPanel mount audit", () => {
  const AUDIT = {
    at_ms: 1,
    identity: { model: "Advanced VX", model_code: 20, firmware: "5.30" },
    site: { read: true, lat_deg: 48.8566, lon_deg: 2.3522 },
    clock: {
      read: true,
      utc: "2026-09-03T21:00:00Z",
      offset_hours: 1,
      dst: true,
      skew_sec: 2,
    },
    drive: {
      read: true,
      tracking: true,
      tracking_rate: "sidereal",
      aligned: true,
    },
    guide: {
      read: true,
      ra_units: 128,
      dec_units: 32,
      ra_fraction: 0.5,
      dec_fraction: 0.125,
      both_axes: true,
      mismatch: true,
    },
    pec: {
      supported: true,
      read: true,
      bins: 4,
      worm_period_sec: 478,
      bin_sec: 5.4,
      lsb_arcsec_per_sec: 0.0147,
      indexed: true,
      current_bin: 0,
      curve: [10, -10, 5, -5],
      all_zero: false,
      peak_units: 10,
      peak_rate_arcsec_per_sec: 0.147,
      swing_arcsec: 3.2,
      net_arcsec_per_rev: 0,
      playback_commanded: false,
    },
    notes: ["the table is not empty"],
  };

  function panelWithStore() {
    const wrapper = mountPanel();
    const store = useCaptureStore();
    vi.mocked(store.auditMount).mockResolvedValue({
      connected: true,
      audit: AUDIT as never,
    });
    vi.mocked(store.resetMount).mockResolvedValue({
      dry_run: true,
      backup_path: "/tmp/mount-restore.json",
      before: AUDIT as never,
      actions: [],
    } as never);
    return { wrapper, store };
  }

  beforeEach(() => {
    posts.length = 0;
    vi.restoreAllMocks();
  });

  // Eighty-eight worm bins is eighty-eight round trips on a 9600-baud link. Polling this during a
  // session would steal commands from whatever else is using the mount.
  it("reads the mount only when asked", async () => {
    const { wrapper, store } = panelWithStore();
    expect(store.auditMount).not.toHaveBeenCalled();

    await wrapper.find('[data-testid="mount-audit-read"]').trigger("click");
    expect(store.auditMount).toHaveBeenCalledTimes(1);
  });

  // A list of signed bytes says nothing; its shape says immediately whether this is a worm
  // correction or noise, and the panel to change it only appears once there is a reading to change.
  it("draws the stored curve, and offers to put it back only after a reading", async () => {
    const { wrapper } = panelWithStore();
    expect(wrapper.find('[data-testid="mount-reset"]').exists()).toBe(false);

    await wrapper.find('[data-testid="mount-audit-read"]').trigger("click");
    await wrapper.vm.$nextTick();

    expect(wrapper.find('[data-testid="mount-reset"]').exists()).toBe(true);
    const path = wrapper.find("svg path");
    expect(path.exists()).toBe(true);
    expect(path.attributes("d")).toContain("M0.00");
  });

  it("previews without applying, and defaults to the settings nothing else can reach", async () => {
    const { wrapper, store } = panelWithStore();
    await wrapper.find('[data-testid="mount-audit-read"]').trigger("click");
    await wrapper.vm.$nextTick();

    await wrapper.find('[data-testid="mount-reset-preview"]').trigger("click");

    expect(store.resetMount).toHaveBeenCalledWith(
      expect.objectContaining({
        apply: false,
        pec: true,
        pec_playback: true,
        guide_rate: true,
        site: false,
        clock: false,
        tracking: false,
      }),
    );
  });

  // Writing to the mount outlives the session and cannot be undone from this panel, so a declined
  // confirmation must send nothing at all rather than sending a dry run.
  it("asks before it writes, and sends nothing when refused", async () => {
    const { wrapper, store } = panelWithStore();
    await wrapper.find('[data-testid="mount-audit-read"]').trigger("click");
    await wrapper.vm.$nextTick();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    await wrapper.find('[data-testid="mount-reset-apply"]').trigger("click");
    expect(confirm).toHaveBeenCalled();
    expect(store.resetMount).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    await wrapper.find('[data-testid="mount-reset-apply"]').trigger("click");
    expect(store.resetMount).toHaveBeenCalledWith(
      expect.objectContaining({ apply: true }),
    );
  });
});
