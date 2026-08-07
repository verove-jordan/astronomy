import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import StackingPanel from "./StackingPanel.vue";
import { testI18n } from "@/test/i18n";
import en from "@/i18n/en.json";
import fr from "@/i18n/fr.json";
import type { StackMenu } from "@/stores/jobs";

// A trimmed copy of what GET /api/mode-params serves (internal/pipeline.StackMenuFor).
const menu: StackMenu = {
  combines: [
    {
      id: "mean",
      engines: ["siril", "native"],
      rejects: true,
      normalizes: true,
    },
    {
      id: "median",
      engines: ["siril", "native"],
      rejects: false,
      normalizes: true,
    },
    {
      id: "sum",
      engines: ["siril", "native"],
      rejects: false,
      normalizes: false,
    },
    {
      id: "trimmed_mean",
      engines: ["native"],
      rejects: true,
      normalizes: true,
    },
  ],
  rejects: [
    { id: "none", engines: ["siril", "native"] },
    {
      id: "percentile",
      engines: ["siril", "native"],
      has_params: true,
      low: { kind: "fraction", default: 0.2, min: 0.01, max: 0.9 },
      high: { kind: "fraction", default: 0.1, min: 0.01, max: 0.9 },
      best_to: 7,
    },
    {
      id: "winsorized",
      engines: ["siril", "native"],
      has_params: true,
      low: { kind: "sigma", default: 3, min: 0.5, max: 10 },
      high: { kind: "sigma", default: 3, min: 0.5, max: 10 },
      best_from: 8,
      best_to: 49,
    },
    {
      id: "gesd",
      engines: ["siril", "native"],
      has_params: true,
      low: { kind: "fraction", default: 0.3, min: 0.01, max: 0.9 },
      high: { kind: "alpha", default: 0.05, min: 0.001, max: 0.5 },
      best_from: 50,
    },
    { id: "entropy_weighted", engines: ["native"] },
  ],
  norms: ["none", "add", "addscale", "mul", "mulscale"],
  weights: ["none", "noise", "wfwhm", "nbstars", "nbstack"],
  auto_bands: [
    { up_to: 7, reject: "percentile" },
    { from: 8, up_to: 49, reject: "winsorized" },
    { from: 50, reject: "gesd" },
  ],
  master_types: [
    "master_bias",
    "master_dark",
    "master_flat",
    "master_dark_flat",
  ],
};

const defaults = {
  stack_engine: "auto",
  stack_combine: "mean",
  stack_reject: "auto",
  stack_reject_low: 0,
  stack_reject_high: 0,
  stack_norm: "addscale",
  stack_weight: "wfwhm",
};

function render(props: Record<string, unknown> = {}) {
  return mount(StackingPanel, {
    props: { menu, defaults, ...props },
    global: { plugins: [testI18n()] },
  });
}

describe("StackingPanel", () => {
  it("renders nothing for a mode that stacks natively (no menu served)", () => {
    const w = mount(StackingPanel, {
      props: { menu: null },
      global: { plugins: [testI18n()] },
    });
    expect(w.find("section").exists()).toBe(false);
  });

  it("builds its dropdowns from the engine's catalogue, not a hardcoded list", () => {
    const w = render();
    const rejectIds = w
      .find('[data-demo="stack-reject"]')
      .findAll("option")
      .map((o) => o.attributes("value"));
    expect(rejectIds).toEqual([
      "auto",
      "none",
      "percentile",
      "winsorized",
      "gesd",
      "entropy_weighted",
    ]);
  });

  it("marks the Go-only algorithms so the engine switch is never a surprise", () => {
    const w = render();
    const opts = w.find('[data-demo="stack-reject"]').findAll("option");
    const entropy = opts.find(
      (o) => o.attributes("value") === "entropy_weighted",
    );
    expect(entropy?.text()).toContain(en.stacking.nativeSuffix);
    const winsor = opts.find((o) => o.attributes("value") === "winsorized");
    expect(winsor?.text()).not.toContain(en.stacking.nativeSuffix);
  });

  it("badges the algorithm the count-adaptive rule would pick for this capture", () => {
    const deep = render({ frameCount: 60 });
    const gesd = deep
      .find('[data-demo="stack-reject"]')
      .findAll("option")
      .find((o) => o.attributes("value") === "gesd");
    expect(gesd?.text()).toContain(en.stacking.recommended);

    const shallow = render({ frameCount: 5 });
    const pct = shallow
      .find('[data-demo="stack-reject"]')
      .findAll("option")
      .find((o) => o.attributes("value") === "percentile");
    expect(pct?.text()).toContain(en.stacking.recommended);
  });

  it("explains the algorithm actually in force, including what auto resolved to", () => {
    const w = render({ frameCount: 60 });
    expect(w.text()).toContain(en.stackAlgo.reject.gesd.label);
    expect(w.text()).toContain(en.stackAlgo.reject.gesd.expect);
  });

  it("labels the rejection parameters in the chosen algorithm's own units", () => {
    const sigma = render({
      params: { ...defaults, stack_reject: "winsorized" },
    });
    expect(sigma.text()).toContain(en.stacking.param.sigma.low);

    // GESD's two numbers are NOT sigmas — an outlier fraction and a significance level.
    const gesd = render({ params: { ...defaults, stack_reject: "gesd" } });
    expect(gesd.text()).toContain(en.stacking.param.alpha.high);
    expect(gesd.text()).not.toContain(en.stacking.param.sigma.high);
  });

  it("shows the algorithm's own default when the knob is left unset", () => {
    const w = render({ params: { ...defaults, stack_reject: "gesd" } });
    const low = w.find('[data-demo="stack-reject-low"]');
    expect((low.element as HTMLInputElement).value).toBe("0.3");
  });

  it("hides the parameters for a method that rejects nothing", () => {
    const w = render({ params: { ...defaults, stack_combine: "sum" } });
    expect(w.find('[data-demo="stack-reject-low"]').exists()).toBe(false);
    expect(w.text()).toContain(en.stacking.noRejection);
  });

  it("disables normalization and weighting where Siril accepts neither", () => {
    const w = render({ params: { ...defaults, stack_combine: "sum" } });
    expect(
      w.find('[data-demo="stack-norm"]').attributes("disabled"),
    ).toBeDefined();
    expect(
      w.find('[data-demo="stack-weight"]').attributes("disabled"),
    ).toBeDefined();
  });

  it("emits a patch of exactly the edited key, for the parent to merge into the params JSON", async () => {
    const w = render();
    await w.find('[data-demo="stack-reject"]').setValue("gesd");
    expect(w.emitted("patch")?.[0]).toEqual([{ stack_reject: "gesd" }]);

    await w.find('[data-demo="stack-weight"]').setValue("noise");
    expect(w.emitted("patch")?.[1]).toEqual([{ stack_weight: "noise" }]);
  });

  it("emits nothing while the params JSON is invalid", async () => {
    const w = render({ disabled: true });
    await w.find('[data-demo="stack-reject"]').setValue("gesd");
    expect(w.emitted("patch")).toBeUndefined();
  });

  it("offers a calibration-master row only for the frame types the capture holds", () => {
    // No calibration frames inspected → no rows at all.
    expect(render().find('[data-demo="master_dark-reject"]').exists()).toBe(
      false,
    );

    const w = render({ frameCounts: { master_dark: 40, master_flat: 12 } });
    expect(w.find('[data-demo="master_dark-reject"]').exists()).toBe(true);
    expect(w.find('[data-demo="master_flat-reject"]').exists()).toBe(true);
    expect(w.find('[data-demo="master_bias-reject"]').exists()).toBe(false);
  });

  it("recommends per frame type from that type's OWN pool depth", () => {
    // 200 bias frames want GESD; 5 flats want percentile. The whole point of per-type recipes.
    const w = render({ frameCounts: { master_bias: 200, master_flat: 5 } });
    const optionFor = (prefix: string, id: string) =>
      w
        .find(`[data-demo="${prefix}-reject"]`)
        .findAll("option")
        .find((o) => o.attributes("value") === id);
    expect(optionFor("master_bias", "gesd")?.text()).toContain(
      en.stacking.recommended,
    );
    expect(optionFor("master_bias", "percentile")?.text()).not.toContain(
      en.stacking.recommended,
    );
    expect(optionFor("master_flat", "percentile")?.text()).toContain(
      en.stacking.recommended,
    );
    expect(optionFor("master_flat", "gesd")?.text()).not.toContain(
      en.stacking.recommended,
    );
  });

  it("emits the frame type's own key, so each recipe is independent", async () => {
    const w = render({ frameCounts: { master_dark: 40 } });
    await w.find('[data-demo="master_dark-reject"]').setValue("gesd");
    expect(w.emitted("patch")?.[0]).toEqual([{ master_dark_reject: "gesd" }]);
  });

  it("labels a master's parameters in its own algorithm's units", () => {
    const w = render({
      frameCounts: { master_bias: 200 },
      params: { ...defaults, master_bias_reject: "gesd" },
    });
    expect(w.find('[data-demo="master_bias-low"]').exists()).toBe(true);
    expect(w.text()).toContain(en.stacking.param.alpha.high);
  });

  it("shows the comet-aligned stack's own rejection only in comet mode", () => {
    expect(render().find('[data-demo="stack-comet-reject"]').exists()).toBe(
      false,
    );
    expect(
      render({ showComet: true })
        .find('[data-demo="stack-comet-reject"]')
        .exists(),
    ).toBe(true);
  });
});

// Every algorithm the engine can offer must be explained in BOTH locales — the whole point of the
// panel is that the user understands what they are picking. A new entry in the Go catalogue that
// nobody wrote copy for fails here rather than rendering a bare id in the dropdown.
describe("stacking algorithm copy", () => {
  const families: Record<string, string[]> = {
    combine: ["mean", "median", "sum", "max", "min", "trimmed_mean"],
    reject: [
      "none",
      "percentile",
      "sigma",
      "median_sigma",
      "winsorized",
      "linear_fit",
      "gesd",
      "mad",
      "rcr",
      "adaptive_weighted",
      "entropy_weighted",
    ],
    norm: ["none", "add", "addscale", "mul", "mulscale"],
    weight: ["none", "noise", "wfwhm", "nbstars", "nbstack"],
  };

  for (const [locale, messages] of Object.entries({ en, fr })) {
    for (const [family, ids] of Object.entries(families)) {
      for (const id of ids) {
        it(`${locale}: ${family}.${id} has a label and a description`, () => {
          const entry = (
            messages.stackAlgo as unknown as Record<
              string,
              Record<string, { label?: string; desc?: string; expect?: string }>
            >
          )[family][id];
          expect(entry?.label, `stackAlgo.${family}.${id}.label`).toBeTruthy();
          expect(entry?.desc, `stackAlgo.${family}.${id}.desc`).toBeTruthy();
          // Only the algorithm choices promise an "expected result"; norm/weight are one-liners.
          if (family === "combine" || family === "reject") {
            expect(
              entry?.expect,
              `stackAlgo.${family}.${id}.expect`,
            ).toBeTruthy();
          }
        });
      }
    }
  }

  it("documents every new stacking knob in both locales", () => {
    const keys = [
      "stack_engine",
      "stack_combine",
      "stack_reject",
      "stack_reject_low",
      "stack_reject_high",
      "stack_trim_frac",
      "stack_norm",
      "stack_fast_norm",
      "stack_weight",
      "stack_rejection_maps",
      "stack_feather",
      "stack_local_norm",
      "stack_local_norm_degree",
      "comet_stack_reject",
      "comet_stack_low",
      "comet_stack_high",
      "master_bias_combine",
      "master_bias_reject",
      "master_bias_low",
      "master_bias_high",
      "master_dark_combine",
      "master_dark_reject",
      "master_dark_low",
      "master_dark_high",
      "master_flat_combine",
      "master_flat_reject",
      "master_flat_low",
      "master_flat_high",
      "master_dark_flat_combine",
      "master_dark_flat_reject",
      "master_dark_flat_low",
      "master_dark_flat_high",
    ];
    for (const [locale, messages] of Object.entries({ en, fr })) {
      const docs = messages.paramDocs as unknown as Record<string, string>;
      for (const k of keys) {
        expect(docs[k], `${locale}: paramDocs.${k}`).toBeTruthy();
      }
    }
  });
});
