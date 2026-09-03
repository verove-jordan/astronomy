import { describe, it, expect, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import HelpButton from "./HelpButton.vue";
import { testI18n } from "@/test/i18n";
import en from "@/i18n/en.json";

// The button reads only route.name, so a stubbed useRoute is enough — a real router would add a
// history, navigation guards and every lazy view chunk for nothing.
const routeName = { value: "" };
vi.mock("vue-router", () => ({
  useRoute: () => ({ name: routeName.value }),
}));

function mountIt(name: string, page?: string) {
  routeName.value = name;
  return mount(HelpButton, {
    props: page ? { page } : {},
    global: { plugins: [testI18n()] },
  });
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("HelpButton", () => {
  it("renders nothing for a page with no tour", () => {
    // Dropping the button into a view before its steps exist must be harmless — otherwise every new
    // page ships a control that opens an empty modal.
    const w = mountIt("no-such-page");
    expect(w.find("button").exists()).toBe(false);
    w.unmount();
  });

  it("offers the tour for a page that has one", () => {
    const w = mountIt("no-such-page", "runs");
    const btn = w.find("button");
    expect(btn.exists()).toBe(true);
    expect(btn.attributes("aria-label")).toBe(en.tour.open);
    w.unmount();
  });

  it("does not open the tour until it is clicked", async () => {
    const w = mountIt("no-such-page", "runs");
    expect(document.body.textContent).not.toContain(en.tour.runs.title);
    await w.find("button").trigger("click");
    expect(document.body.textContent).toContain(en.tour.runs.title);
    w.unmount();
  });
});
