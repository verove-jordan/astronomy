import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import EyepieceKitTable from "./EyepieceKitTable.vue";
import { testI18n } from "@/test/i18n";
import type { SkyEyepiece } from "@/types";

const KIT: SkyEyepiece[] = [
  { label: "30mm", focal_mm: 30, afov_deg: 68 },
  { label: "18mm", focal_mm: 18, afov_deg: 65 },
  { label: "10mm", focal_mm: 10, afov_deg: 60 },
  { label: "6mm", focal_mm: 6, afov_deg: 60 },
];

// 740 mm behind a ×0.66 reducer — the case the reducer field exists for.
const EFF_REDUCED = 488.4;

function mountIt(
  modelValue: SkyEyepiece[] = KIT,
  effectiveFocalMm = 740,
  apertureMm = 100,
) {
  return mount(EyepieceKitTable, {
    props: { modelValue, effectiveFocalMm, apertureMm },
    global: { plugins: [testI18n()] },
  });
}

const emitted = (w: ReturnType<typeof mountIt>): SkyEyepiece[] => {
  const events = w.emitted("update:modelValue");
  if (!events?.length) throw new Error("no update:modelValue emitted");
  return events[events.length - 1][0] as SkyEyepiece[];
};

describe("EyepieceKitTable", () => {
  it("names its columns in a header row", () => {
    const headers = mountIt()
      .findAll("th")
      .map((h) => h.text());
    expect(headers).toEqual([
      "Focal (mm)",
      "Apparent field (°)",
      "Label",
      "Power",
      "True field",
      "Exit pupil",
      "Remove", // sr-only
    ]);
  });

  it("renders one row per eyepiece with its editable values", () => {
    const rows = mountIt().findAll("tbody tr");
    expect(rows).toHaveLength(4);
    const inputs = rows[0].findAll("input");
    expect((inputs[0].element as HTMLInputElement).value).toBe("30");
    expect((inputs[1].element as HTMLInputElement).value).toBe("68");
    expect((inputs[2].element as HTMLInputElement).value).toBe("30mm");
  });

  it("shows what each eyepiece gives, recomputed from the effective focal length", () => {
    const cells = (eff: number) =>
      mountIt(KIT, eff)
        .findAll("tbody tr")
        .map((r) =>
          r
            .findAll("td")
            .slice(3, 6)
            .map((c) => c.text()),
        );

    // At the native 740 mm the 10 mm eyepiece is the engine's pinned 74× / 0.81° / 1.4 mm.
    expect(cells(740)[2]).toEqual(["74×", "0.81°", "1.4 mm"]);

    // Behind the ×0.66 reducer every row drops in power and gains exit pupil.
    expect(cells(EFF_REDUCED)).toEqual([
      ["16×", "4.18°", "6.1 mm"],
      ["27×", "2.40°", "3.7 mm"],
      ["49×", "1.23°", "2.0 mm"],
      ["81×", "0.74°", "1.2 mm"],
    ]);
  });

  it("flags an exit pupil outside the comfortable window", () => {
    const ok = mountIt(KIT, EFF_REDUCED);
    expect(ok.find('[role="status"]').exists()).toBe(false);

    // A 40 mm eyepiece behind the reducer: 12× → 8.2 mm, past the dark-adapted eye's limit.
    const wide = mountIt(
      [...KIT, { label: "40mm", focal_mm: 40, afov_deg: 70 }],
      EFF_REDUCED,
    );
    const warning = wide.find('[role="status"]');
    expect(warning.exists()).toBe(true);
    expect(warning.text()).toContain("0.5");
    expect(wide.findAll("tbody tr")[4].findAll("td")[5].classes()).toContain(
      "text-amber-600",
    );
  });

  it("leaves a row blank rather than showing NaN while it is half-typed", () => {
    const w = mountIt([{ label: "", focal_mm: 0, afov_deg: 60 }]);
    const cells = w.findAll("tbody tr")[0].findAll("td");
    expect(cells.slice(3, 6).map((c) => c.text())).toEqual(["—", "—", "—"]);
  });

  it("emits a new array on edit without mutating the source", async () => {
    const source = KIT.map((e) => ({ ...e }));
    const w = mountIt(source);
    await w.findAll("tbody tr")[0].findAll("input")[0].setValue("32");

    const next = emitted(w);
    expect(next[0].focal_mm).toBe(32);
    expect(next[0].label).toBe("30mm"); // untouched fields survive
    expect(next).not.toBe(source);
    expect(source[0].focal_mm).toBe(30); // the prop array is never written through
  });

  it("treats a cleared number as 0 so the row is simply incomplete", async () => {
    const w = mountIt();
    await w.findAll("tbody tr")[0].findAll("input")[0].setValue("");
    expect(emitted(w)[0].focal_mm).toBe(0);
  });

  it("adds and removes rows", async () => {
    const w = mountIt();
    await w.find("button").trigger("click"); // "+ Add eyepiece"
    const added = emitted(w);
    expect(added).toHaveLength(5);
    expect(added[4]).toEqual({ label: "", focal_mm: 0, afov_deg: 60 });

    const w2 = mountIt();
    await w2.findAll("tbody tr")[1].find("button").trigger("click");
    expect(emitted(w2).map((e) => e.focal_mm)).toEqual([30, 10, 6]);
  });

  it("invites the user to add one when the kit is empty", () => {
    const w = mountIt([]);
    expect(w.findAll("tbody tr")).toHaveLength(1);
    expect(w.text()).toContain("Add at least one eyepiece.");
  });
});
