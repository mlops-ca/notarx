import { renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { createFormatters } from "./format";
import { PreferencesProvider } from "./preferences";
import { useFormatters } from "./useFormatters";

// Intl output uses no-break and narrow no-break spaces; compare with plain ones.
const plain = (text: string) => text.replace(/[\u00a0\u202f]/g, " ");

const instant = "2026-10-14T15:04:05Z";

describe("createFormatters", () => {
  it.each([
    ["en-CA", "1,234,567.5", "1.5 GB", "a, b and c"],
    ["fr-CA", "1 234 567,5", "1,5 Go", "a, b et c"],
    ["zh-CN", "1,234,567.5", "1.5 GB", "a、b和c"],
  ])(
    "formats numbers, bytes and lists for %s",
    (locale, number, bytes, list) => {
      const formatters = createFormatters(locale);
      expect(plain(formatters.formatNumber(1234567.5))).toBe(number);
      expect(plain(formatters.formatBytes(1_500_000_000))).toBe(bytes);
      expect(plain(formatters.list.format(["a", "b", "c"]))).toBe(list);
    },
  );

  it("formats dates and times in the locale", () => {
    const options = { timeZone: "UTC" } as const;
    expect(
      plain(createFormatters("en-CA").formatDateTime(instant, options)),
    ).toBe("2026-10-14, 3:04:05 p.m.");
    expect(
      plain(createFormatters("fr-CA").formatDateTime(instant, options)),
    ).toBe("2026-10-14 15 h 04 min 05 s");
    expect(
      plain(createFormatters("zh-CN").formatDateTime(instant, options)),
    ).toBe("2026/10/14 15:04:05");
  });

  it("formats relative times in the locale", () => {
    expect(createFormatters("en-CA").relativeTime.format(-1, "day")).toBe(
      "yesterday",
    );
    expect(createFormatters("fr-CA").relativeTime.format(-1, "day")).toBe(
      "hier",
    );
    expect(createFormatters("en-CA").relativeTime.format(3, "hour")).toBe(
      "in 3 hours",
    );
    expect(createFormatters("fr-CA").relativeTime.format(3, "hour")).toBe(
      "dans 3 heures",
    );
  });

  it("scales byte sizes and marks missing or invalid values", () => {
    const en = createFormatters("en-CA");
    expect(en.formatBytes(0)).toBe("0 bytes");
    expect(plain(en.formatBytes(999))).toBe("999 bytes");
    expect(plain(en.formatBytes(1500))).toBe("1.5 kB");
    expect(en.formatBytes(undefined)).toBe("—");
    expect(en.formatBytes(Number.NaN)).toBe("—");
    expect(en.formatDateTime(undefined)).toBe("—");
    expect(en.formatDateTime("not a date")).toBe("not a date");
    expect(en.formatNumber(undefined)).toBe("—");
  });

  it("selects plural forms per locale; fr-CA covers 0 and 1 with one", () => {
    const en = createFormatters("en-CA").plural;
    const fr = createFormatters("fr-CA").plural;
    expect([0, 1, 2].map((count) => en.select(count))).toEqual([
      "other",
      "one",
      "other",
    ]);
    expect([0, 1, 2].map((count) => fr.select(count))).toEqual([
      "one",
      "one",
      "other",
    ]);
  });
});

describe("useFormatters", () => {
  afterEach(() => localStorage.clear());

  function wrapper({ children }: { children: ReactNode }) {
    return createElement(PreferencesProvider, null, children);
  }

  it("binds the formatters to the active Console locale", () => {
    localStorage.setItem("ag.console.locale", "fr-CA");
    const { result } = renderHook(() => useFormatters(), { wrapper });
    expect(result.current.locale).toBe("fr-CA");
    expect(plain(result.current.formatNumber(1234.5))).toBe("1 234,5");
  });

  it("defaults to en-CA", () => {
    const { result } = renderHook(() => useFormatters(), { wrapper });
    expect(result.current.locale).toBe("en-CA");
    expect(result.current.formatNumber(1234.5)).toBe("1,234.5");
  });
});
