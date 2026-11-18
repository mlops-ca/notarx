import { renderHook, waitFor } from "@testing-library/react";
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
    ["en-CA", "1,234,567.5", "1.4 GiB", "a, b and c"],
    ["fr-CA", "1 234 567,5", "1,4 Gio", "a, b et c"],
    ["zh-CN", "1,234,567.5", "1.4 GiB", undefined],
  ])(
    "formats numbers, bytes and lists for %s",
    (locale, number, bytes, list) => {
      const formatters = createFormatters(locale);
      expect(plain(formatters.formatNumber(1234567.5))).toBe(number);
      expect(plain(formatters.formatBytes(1_500_000_000))).toBe(bytes);
      if (list) {
        expect(plain(formatters.list.format(["a", "b", "c"]))).toBe(list);
      }
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
    const fr = createFormatters("fr-CA");
    expect(en.formatBytes(0)).toBe("0 B");
    expect(plain(en.formatBytes(999))).toBe("999 B");
    expect(plain(en.formatBytes(1024))).toBe("1.0 KiB");
    expect(plain(en.formatBytes(1536))).toBe("1.5 KiB");
    expect(plain(en.formatBytes(150 * 1024))).toBe("150 KiB");
    expect(plain(fr.formatBytes(1536))).toBe("1,5 Kio");
    expect(plain(fr.formatBytes(512))).toBe("512 o");
    // A value that rounds up to 1024 moves to the next unit.
    expect(plain(en.formatBytes(1024 * 1024 - 1))).toBe("1.0 MiB");
    expect(plain(en.formatBytes(1024 * 1023.4))).toBe("1,023 KiB");
    expect(plain(en.formatBytes(1024 * 1023.6))).toBe("1.0 MiB");
    expect(plain(en.formatBytes(1024 ** 3 * 256))).toBe("256 GiB");
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

  it("binds the formatters to the active Console locale", async () => {
    localStorage.setItem("ag.console.locale", "zh-CN");
    const { result } = renderHook(() => useFormatters(), { wrapper });
    // The stored language shows once its message table has loaded.
    await waitFor(() => expect(result.current.locale).toBe("zh-CN"));
    expect(
      plain(result.current.formatDateTime(instant, { timeZone: "UTC" })),
    ).toBe("2026/10/14 15:04:05");
  });

  it("defaults to en-CA", () => {
    const { result } = renderHook(() => useFormatters(), { wrapper });
    expect(result.current.locale).toBe("en-CA");
    expect(result.current.formatNumber(1234.5)).toBe("1,234.5");
  });
});
