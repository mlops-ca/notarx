export function formatDate(iso: string | undefined, locale?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function shortDigest(digest: string | undefined): string {
  if (!digest) return "—";
  if (digest.length <= 19) return digest;
  return `${digest.slice(0, 19)}…`;
}

export function formatNumber(n: number | undefined, locale?: string): string {
  if (n === undefined || n === null) return "—";
  return new Intl.NumberFormat(locale).format(n);
}

const DATE_TIME_OPTIONS: Intl.DateTimeFormatOptions = {
  year: "numeric",
  month: "numeric",
  day: "numeric",
  hour: "numeric",
  minute: "numeric",
  second: "numeric",
};

/** Binary (IEC) storage units; the symbols come per language, the numbers from Intl. */
const BINARY_UNIT_SYMBOLS = {
  en: ["B", "KiB", "MiB", "GiB", "TiB", "PiB"],
  fr: ["o", "Kio", "Mio", "Gio", "Tio", "Pio"],
} as const;

/** A byte-size formatter: binary units, "—" for a missing or invalid size. */
export type FormatBytes = (bytes: number | undefined) => string;

/** The Intl formatters, bound to one locale tag (see `useFormatters`). */
export interface Formatters {
  /** The BCP 47 tag every formatter is bound to. */
  locale: string;
  number: Intl.NumberFormat;
  /** Numeric date and time, what `toLocaleString()` shows for a locale. */
  dateTime: Intl.DateTimeFormat;
  relativeTime: Intl.RelativeTimeFormat;
  list: Intl.ListFormat;
  /** Plural categories; fr-CA reports `one` for 0 and 1, en-CA only for 1. */
  plural: Intl.PluralRules;
  /**
   * A date and time; "—" when there is none, and the input text unchanged
   * when it does not parse (as `formatDate` does).
   */
  formatDateTime(
    value: string | number | Date | undefined,
    options?: Intl.DateTimeFormatOptions,
  ): string;
  /** A number with the locale's grouping and decimal separators. */
  formatNumber(
    value: number | undefined,
    options?: Intl.NumberFormatOptions,
  ): string;
  /**
   * A storage size in IEC binary units (1 KiB is 1024 bytes; Kio, Mio in
   * fr-CA). The digits and separators come from Intl and the symbols from the
   * language, so one convention serves every table, meter and chart.
   */
  formatBytes: FormatBytes;
}

/** Builds the formatters for a locale tag; `useFormatters` memoizes it per locale. */
export function createFormatters(locale: string): Formatters {
  const number = new Intl.NumberFormat(locale);
  const dateTime = new Intl.DateTimeFormat(locale, DATE_TIME_OPTIONS);
  const symbols = BINARY_UNIT_SYMBOLS[locale.startsWith("fr") ? "fr" : "en"];
  const space = locale.startsWith("fr") ? "\u202f" : " ";
  // One decimal below 100 and none from 100 up, as the Console tables always showed.
  const sizes = [
    new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }),
    new Intl.NumberFormat(locale, {
      minimumFractionDigits: 1,
      maximumFractionDigits: 1,
    }),
  ];
  return {
    locale,
    number,
    dateTime,
    relativeTime: new Intl.RelativeTimeFormat(locale, { numeric: "auto" }),
    list: new Intl.ListFormat(locale, { style: "long", type: "conjunction" }),
    plural: new Intl.PluralRules(locale),
    formatDateTime(value, options) {
      if (value === undefined || value === null || value === "") return "—";
      const date = value instanceof Date ? value : new Date(value);
      if (Number.isNaN(date.getTime())) return String(value);
      return options
        ? new Intl.DateTimeFormat(locale, {
            ...DATE_TIME_OPTIONS,
            ...options,
          }).format(date)
        : dateTime.format(date);
    },
    formatNumber(value, options) {
      if (value === undefined || value === null) return "—";
      return options
        ? new Intl.NumberFormat(locale, options).format(value)
        : number.format(value);
    },
    formatBytes(size) {
      if (size === undefined || size === null || Number.isNaN(size)) return "—";
      const last = symbols.length - 1;
      let exponent =
        size === 0
          ? 0
          : Math.max(
              0,
              Math.min(Math.floor(Math.log2(Math.abs(size)) / 10), last),
            );
      // A value that rounds up to 1024 belongs to the next unit (1024 KiB is 1 MiB).
      if (
        exponent < last &&
        Math.abs(Math.round(size / 1024 ** exponent)) >= 1024
      ) {
        exponent += 1;
      }
      if (size === 0) return `0${space}${symbols[0]}`;
      const value = size / 1024 ** exponent;
      const digits = Math.abs(value) >= 100 ? 0 : 1;
      return `${sizes[digits].format(value)}${space}${symbols[exponent]}`;
    },
  };
}
