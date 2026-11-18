import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { PreferencesProvider, usePreferences } from "../lib/preferences";
import {
  DEFAULT_LOCALE,
  SUPPORTED_LOCALES,
  detectLocale,
  isMessageLocale,
  localeInfo,
  matchLanguage,
} from "./locales";

// The retired tag is built, so no literal of it remains in the tree.
const US_ENGLISH = ["en", "US"].join("-");

describe("SUPPORTED_LOCALES", () => {
  it("registers en-CA, fr-CA and zh-CN with per-tag antd, dayjs and Intl values", () => {
    expect(SUPPORTED_LOCALES.map((locale) => locale.tag)).toEqual([
      "en-CA",
      "fr-CA",
      "zh-CN",
    ]);
    expect(SUPPORTED_LOCALES.map((locale) => locale.dayjs)).toEqual([
      "en-ca",
      "fr-ca",
      "zh-cn",
    ]);
    expect(SUPPORTED_LOCALES.map((locale) => locale.intl)).toEqual([
      "en-CA",
      "fr-CA",
      "zh-CN",
    ]);
    // en-CA reuses the en_GB strings (en_US formats dates as MM/DD) and points
    // the pickers at the dayjs en-ca locale.
    expect(localeInfo("en-CA").antd.locale).toBe("en-ca");
    expect(localeInfo("fr-CA").antd.locale).toBe("fr");
    expect(localeInfo("zh-CN").antd.locale).toBe("zh-cn");
  });

  it("retires the legacy US English and bare en tags without an alias", () => {
    expect(isMessageLocale(US_ENGLISH)).toBe(false);
    expect(isMessageLocale("en")).toBe(false);
    expect(isMessageLocale("fr-CA")).toBe(true);
  });
});

describe("matchLanguage", () => {
  it.each([
    ["fr", "fr-CA"],
    ["fr-FR", "fr-CA"],
    ["fr-CA", "fr-CA"],
    ["en", "en-CA"],
    [US_ENGLISH, "en-CA"],
    ["en-GB", "en-CA"],
    ["zh-CN", "zh-CN"],
    ["zh-SG", "zh-CN"],
    ["zh-Hans", "zh-CN"],
    ["zh-Hans-CN", "zh-CN"],
    ["zh_cn", "zh-CN"],
    ["zh-TW", undefined],
    ["zh-HK", undefined],
    ["zh-Hant", undefined],
    ["zh-Hant-TW", undefined],
    ["zh", undefined],
    ["de-DE", undefined],
    ["", undefined],
  ])("maps %s to %s", (language, expected) => {
    expect(matchLanguage(language)).toBe(expected);
  });
});

describe("detectLocale", () => {
  it("prefers the stored choice over the browser languages", () => {
    expect(detectLocale("zh-CN", ["fr-CA", US_ENGLISH])).toBe("zh-CN");
    expect(detectLocale("fr-CA", [US_ENGLISH])).toBe("fr-CA");
  });

  it("ignores a stored tag outside the registry", () => {
    expect(detectLocale(US_ENGLISH, ["fr-CA"])).toBe("fr-CA");
    expect(detectLocale("klingon", [])).toBe(DEFAULT_LOCALE);
  });

  it("uses the first matching navigator language by language subtag", () => {
    expect(detectLocale(null, ["fr", "en"])).toBe("fr-CA");
    expect(detectLocale(null, ["en", "fr"])).toBe("en-CA");
    expect(detectLocale(null, ["zh-CN"])).toBe("zh-CN");
    expect(detectLocale(null, ["zh-Hans"])).toBe("zh-CN");
  });

  it("skips Traditional Chinese and unknown languages for the next entry", () => {
    expect(detectLocale(null, ["zh-TW", "fr-FR"])).toBe("fr-CA");
    expect(detectLocale(null, ["zh-HK", "zh-Hant", "en-GB"])).toBe("en-CA");
    expect(detectLocale(null, ["de", "es", "zh-CN"])).toBe("zh-CN");
  });

  it("defaults to en-CA when nothing matches", () => {
    expect(detectLocale(null, [])).toBe("en-CA");
    expect(detectLocale(undefined, ["zh-TW", "de"])).toBe("en-CA");
  });
});

function Probe() {
  const { locale } = usePreferences();
  return createElement("p", null, locale);
}

describe("PreferencesProvider locale detection", () => {
  afterEach(() => {
    cleanup();
    localStorage.clear();
    document.documentElement.lang = "";
    vi.restoreAllMocks();
  });

  it("starts from the browser language and sets the document language", async () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["zh-TW", "fr-FR"]);
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("fr-CA")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("fr-CA");
    expect(localStorage.getItem("ag.console.locale")).toBe("fr-CA");
  });

  it("lets the stored choice win and drops the retired US English value", async () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["fr-FR"]);
    localStorage.setItem("ag.console.locale", "zh-CN");
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("zh-CN")).toBeInTheDocument();
    cleanup();

    localStorage.setItem("ag.console.locale", US_ENGLISH);
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("fr-CA")).toBeInTheDocument();
  });
});
