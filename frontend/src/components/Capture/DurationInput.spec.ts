import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import DurationInput from "./DurationInput.vue";

// The field stores microseconds and is typed in a unit the user picks. What broke was neither of
// those: the input was rebuilt from the model on every render, so a half-typed "0." came back as "0"
// and no fractional exposure could be entered while a run updated the panel once a second.

function mountInput(modelValue = 60_000_000) {
  return mount(DurationInput, { props: { modelValue } });
}

async function type(
  wrapper: ReturnType<typeof mountInput>,
  ...keystrokes: string[]
) {
  const el = wrapper.find("input");
  await el.trigger("focus");
  for (const text of keystrokes) {
    (el.element as HTMLInputElement).value = text;
    await el.trigger("input");
  }
  return el;
}

function lastEmitted(wrapper: ReturnType<typeof mountInput>): number | null {
  const events = wrapper.emitted("update:modelValue");
  return events ? (events[events.length - 1][0] as number) : null;
}

describe("DurationInput", () => {
  it("shows a duration in the unit it reads most naturally in", () => {
    const cases: [number, string, string][] = [
      [60_000_000, "60", "s"],
      [1_500_000, "1.5", "s"],
      [5_000, "5", "ms"],
      [32, "32", "us"], // a bias frame, which is where microseconds stop being theoretical
    ];
    for (const [us, text, unit] of cases) {
      const wrapper = mountInput(us);
      expect(wrapper.find("input").element.value).toBe(text);
      expect(wrapper.find("select").element.value).toBe(unit);
    }
  });

  it("accepts a fraction typed one character at a time", async () => {
    const wrapper = mountInput();
    await type(wrapper, "0", "0.", "0.5");
    expect(lastEmitted(wrapper)).toBe(500_000);
  });

  it("keeps the half-typed decimal through an unrelated re-render", async () => {
    // The bug. The parent re-renders once a second while a session runs, and every render used to
    // rewrite the field from the model — deleting the "." as fast as it could be typed.
    const wrapper = mountInput();
    const el = await type(wrapper, "0.");
    await wrapper.setProps({ modelValue: 1 });
    expect((el.element as HTMLInputElement).value).toBe("0.");
  });

  it("takes a value changed from elsewhere while it is not being edited", async () => {
    const wrapper = mountInput();
    await wrapper.setProps({ modelValue: 2_500_000 }); // a bulk edit
    expect(wrapper.find("input").element.value).toBe("2.5");
  });

  it("stores microseconds when microseconds are chosen", async () => {
    const wrapper = mountInput();
    await wrapper.find("select").setValue("us");
    await type(wrapper, "32");
    expect(lastEmitted(wrapper)).toBe(32);
  });

  it("re-expresses the duration on a unit change rather than reinterpreting the number", async () => {
    // 60 s must become 60000 ms, never 60 ms. The other reading silently turns a minute-long sub
    // into a millisecond one, and nobody finds out until the stack comes back empty.
    const wrapper = mountInput(60_000_000);
    await wrapper.find("select").setValue("ms");

    expect(wrapper.find("input").element.value).toBe("60000");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
  });

  it("refuses a zero-length exposure, which the sequencer rejects too", async () => {
    const wrapper = mountInput();
    await type(wrapper, "0");
    expect(lastEmitted(wrapper)).toBe(1);
  });

  it("leaves the stored value alone while the field is empty", async () => {
    const wrapper = mountInput();
    await type(wrapper, "");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
  });

  it("shows what was actually stored once editing ends", async () => {
    const wrapper = mountInput();
    const el = await type(wrapper, "0.");
    await el.trigger("blur");
    expect((el.element as HTMLInputElement).value).toBe("60");
  });
});
