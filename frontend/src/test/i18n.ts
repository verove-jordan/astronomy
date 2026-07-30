// Shared vue-i18n instance for component specs: the REAL English messages, so tests assert the
// user-facing strings (and break when a key referenced by a component is missing from en.json).
import { createI18n } from "vue-i18n";
import en from "@/i18n/en.json";

export function testI18n() {
  return createI18n({
    legacy: false,
    locale: "en",
    fallbackLocale: "en",
    messages: { en },
  });
}
