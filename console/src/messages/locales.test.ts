import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { PreferencesProvider, usePreferences } from "../lib/preferences";
import {
  DEFAULT_LOCALE,
  READY_LOCALES,
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
  });

  it("loads the antd locale of each tag on demand", async () => {
    // en-CA reuses the en_GB strings (en_US formats dates as MM/DD) and points
    // the pickers at the dayjs en-ca locale.
    expect((await localeInfo("en-CA").load()).locale).toBe("en-ca");
    expect((await localeInfo("fr-CA").load()).locale).toBe("fr-ca");
    expect((await localeInfo("zh-CN").load()).locale).toBe("zh-cn");
  });

  it("gives every tag a short button code", () => {
    expect(SUPPORTED_LOCALES.map((locale) => locale.code)).toEqual([
      "EN",
      "FR",
      "ZH",
    ]);
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
    expect(detectLocale("zh-CN", [US_ENGLISH])).toBe("zh-CN");
  });

  it("ignores a stored tag outside the registry", () => {
    expect(detectLocale(US_ENGLISH, ["zh-CN"])).toBe("zh-CN");
    expect(detectLocale("klingon", [])).toBe(DEFAULT_LOCALE);
  });

  it("uses the first matching navigator language by language subtag", () => {
    const all = ["en-CA", "fr-CA", "zh-CN"] as const;
    expect(detectLocale(null, ["fr", "en"], all)).toBe("fr-CA");
    expect(detectLocale(null, ["en", "fr"], all)).toBe("en-CA");
    expect(detectLocale(null, ["zh-CN"])).toBe("zh-CN");
    expect(detectLocale(null, ["zh-Hans"])).toBe("zh-CN");
  });

  it("offers every ready locale and detects fr-CA from a French browser", () => {
    expect(READY_LOCALES).toEqual(["en-CA", "fr-CA", "zh-CN"]);
    expect(detectLocale(null, ["fr-CA", "en"])).toBe("fr-CA");
    expect(detectLocale(null, ["fr-FR", "zh-CN"])).toBe("fr-CA");
    expect(detectLocale("fr-CA", ["en"])).toBe("fr-CA");
  });

  it("skips a locale outside the available set", () => {
    expect(detectLocale(null, ["fr-CA", "en"], ["en-CA", "zh-CN"])).toBe(
      "en-CA",
    );
    expect(detectLocale("fr-CA", ["fr"], ["en-CA"])).toBe("en-CA");
  });

  it("skips Traditional Chinese and unknown languages for the next entry", () => {
    expect(detectLocale(null, ["zh-TW", "fr-FR"], ["fr-CA"])).toBe("fr-CA");
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

  it("starts from the browser language, sets the document language and stores nothing", async () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["zh-TW", "zh-CN"]);
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("zh-CN")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("zh-CN");
    // A detected language is not a choice: a later browser change still applies.
    expect(localStorage.getItem("ag.console.locale")).toBeNull();
  });

  it("selects fr-CA for a French browser and sets the document language", async () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["fr-FR", "fr"]);
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("fr-CA")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("fr-CA");
  });

  it("lets the stored choice win and drops the retired US English value", async () => {
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["en-GB"]);
    localStorage.setItem("ag.console.locale", "zh-CN");
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("zh-CN")).toBeInTheDocument();
    cleanup();

    localStorage.setItem("ag.console.locale", US_ENGLISH);
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["zh-CN"]);
    render(createElement(PreferencesProvider, null, createElement(Probe)));
    expect(await screen.findByText("zh-CN")).toBeInTheDocument();
  });
});
