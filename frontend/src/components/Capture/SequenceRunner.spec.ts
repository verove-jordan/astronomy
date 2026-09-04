import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createTestingPinia } from "@pinia/testing";
import { vi } from "vitest";
import SequenceRunner from "./SequenceRunner.vue";
import StepBulkEdit from "./StepBulkEdit.vue";
import { testI18n } from "@/test/i18n";
import type { CaptureStep } from "@/types";

// The two edits that stop a night being shot at the wrong settings: a new filter must inherit what
// the previous one uses, and a bulk change must touch only the values it was told to.

function mountRunner() {
  return mount(SequenceRunner, {
    global: {
      plugins: [testI18n(), createTestingPinia({ createSpy: vi.fn })],
      stubs: { DestinationPicker: true, ProgressBar: true },
    },
  });
}

function steps(wrapper: ReturnType<typeof mountRunner>): CaptureStep[] {
  return (wrapper.vm as unknown as { steps: CaptureStep[] }).steps;
}

describe("SequenceRunner", () => {
  let wrapper: ReturnType<typeof mountRunner>;

  beforeEach(() => {
    wrapper = mountRunner();
  });

  it("starts with one filter", () => {
    expect(steps(wrapper)).toHaveLength(1);
  });

  it("gives a new filter the previous one's settings", async () => {
    const first = steps(wrapper)[0];
    first.count = 33;
    first.exposure_us = 45_000_000;
    first.gain = 222;
    first.dither_n = 2;

    const vm = wrapper.vm as unknown as { addStep: () => void };
    vm.addStep();
    await wrapper.vm.$nextTick();

    const added = steps(wrapper)[1];
    expect(added.count).toBe(33);
    expect(added.exposure_us).toBe(45_000_000);
    expect(added.gain).toBe(222);
    expect(added.dither_n).toBe(2);
    expect(added.filter).not.toBe(first.filter);
  });

  it("picks an unused filter name for each new row", async () => {
    const vm = wrapper.vm as unknown as { addStep: () => void };
    vm.addStep();
    vm.addStep();
    await wrapper.vm.$nextTick();

    const names = steps(wrapper).map((s) => s.filter);
    expect(new Set(names).size).toBe(names.length);
  });

  it("applies a bulk change only to the ticked fields", async () => {
    const vm = wrapper.vm as unknown as {
      addStep: () => void;
      applyBulk: (p: {
        count?: number;
        exposure_us?: number;
        gain?: number;
      }) => void;
    };
    vm.addStep();
    await wrapper.vm.$nextTick();
    const before = steps(wrapper).map((s) => ({ ...s }));

    vm.applyBulk({ exposure_us: 300_000_000 });
    await wrapper.vm.$nextTick();

    for (const [i, s] of steps(wrapper).entries()) {
      expect(s.exposure_us).toBe(300_000_000);
      expect(s.count).toBe(before[i].count);
      expect(s.gain).toBe(before[i].gain);
    }
  });

  it("applies a bulk change only to the selected rows", async () => {
    const vm = wrapper.vm as unknown as {
      addStep: () => void;
      toggleRow: (i: number, on: boolean) => void;
      applyBulk: (p: { gain?: number }) => void;
    };
    vm.addStep();
    await wrapper.vm.$nextTick();
    const untouchedGain = steps(wrapper)[0].gain;

    vm.toggleRow(1, true);
    vm.applyBulk({ gain: 400 });
    await wrapper.vm.$nextTick();

    expect(steps(wrapper)[0].gain).toBe(untouchedGain);
    expect(steps(wrapper)[1].gain).toBe(400);
  });

  it("keeps the selection pointing at the same rows after a delete", async () => {
    const vm = wrapper.vm as unknown as {
      addStep: () => void;
      toggleRow: (i: number, on: boolean) => void;
      removeStep: (i: number) => void;
      applyBulk: (p: { gain?: number }) => void;
    };
    vm.addStep();
    vm.addStep();
    await wrapper.vm.$nextTick();
    steps(wrapper)[2].gain = 111;

    vm.toggleRow(2, true); // select the third row…
    vm.removeStep(0); // …then delete the first
    vm.applyBulk({ gain: 500 });
    await wrapper.vm.$nextTick();

    const rows = steps(wrapper);
    expect(rows).toHaveLength(2);
    expect(rows[1].gain).toBe(500);
    expect(rows[0].gain).not.toBe(500);
  });
});

describe("StepBulkEdit", () => {
  it("emits only the ticked values", async () => {
    const wrapper = mount(StepBulkEdit, {
      props: { count: 3 },
      global: { plugins: [testI18n()] },
    });
    const vm = wrapper.vm as unknown as {
      useGain: boolean;
      gainValue: number;
      apply: () => void;
    };
    vm.useGain = true;
    vm.gainValue = 250;
    vm.apply();

    const emitted = wrapper.emitted("apply");
    expect(emitted).toBeTruthy();
    expect(emitted?.[0][0]).toEqual({
      count: undefined,
      exposure_us: undefined,
      gain: 250,
    });
  });

  it("refuses to emit when nothing is ticked", () => {
    const wrapper = mount(StepBulkEdit, {
      props: { count: 2 },
      global: { plugins: [testI18n()] },
    });
    (wrapper.vm as unknown as { apply: () => void }).apply();
    expect(wrapper.emitted("apply")).toBeFalsy();
  });
});

// A sub-second exposure must survive being typed while the panel is re-rendering, which it does once
// a second for the whole of a run. The field used to be rebuilt from the model on every render, so
// the decimal point was deleted as fast as it could be entered and the row collapsed back to a whole
// number — or to 0, which the sequencer then refuses outright.
describe("SequenceRunner exposure field", () => {
  it("keeps a fractional exposure typed during a re-render", async () => {
    const wrapper = mountRunner();
    const field = wrapper.findComponent({ name: "DurationInput" });
    const el = field.find("input");

    await el.trigger("focus");
    for (const text of ["0", "0.", "0.5"]) {
      (el.element as HTMLInputElement).value = text;
      await el.trigger("input");
      steps(wrapper)[0].count += 1; // something else on the panel moves, as progress does
      await wrapper.vm.$nextTick();
    }

    expect(steps(wrapper)[0].exposure_us).toBe(500_000);
    expect((el.element as HTMLInputElement).value).toBe("0.5");
  });
});
