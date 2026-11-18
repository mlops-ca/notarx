export function formatBytes(bytes: number | undefined): string {
  if (bytes === undefined || bytes === null || Number.isNaN(bytes)) return "—";
  if (bytes === 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  const exp = Math.min(Math.floor(Math.log2(bytes) / 10), units.length - 1);
  const value = bytes / 2 ** (exp * 10);
  return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)} ${units[exp]}`;
}

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

const BYTE_UNITS = [
  "byte",
  "kilobyte",
  "megabyte",
  "gigabyte",
  "terabyte",
  "petabyte",
] as const;

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
   * A byte size in decimal SI units; the separators and unit names come from
   * Intl (`1.5 GB` in en-CA, `1,5 Go` in fr-CA). The module-level
   * `formatBytes` keeps its binary KiB units for the existing tables; use one
   * system per screen.
   */
  formatBytes(bytes: number | undefined): string;
}

/** Builds the formatters for a locale tag; `useFormatters` memoizes it per locale. */
export function createFormatters(locale: string): Formatters {
  const number = new Intl.NumberFormat(locale);
  const dateTime = new Intl.DateTimeFormat(locale, DATE_TIME_OPTIONS);
  const bytes = BYTE_UNITS.map(
    (unit) =>
      new Intl.NumberFormat(locale, {
        style: "unit",
        unit,
        unitDisplay: "short",
        maximumFractionDigits: 1,
      }),
  );
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
      const last = BYTE_UNITS.length - 1;
      let exponent =
        size === 0
          ? 0
          : Math.max(
              0,
              Math.min(Math.floor(Math.log10(Math.abs(size)) / 3), last),
            );
      // A value that rounds up to 1000 belongs to the next unit (1000 kB is 1 MB).
      if (
        exponent < last &&
        Math.abs(Math.round((size / 1000 ** exponent) * 10) / 10) >= 1000
      ) {
        exponent += 1;
      }
      return bytes[exponent].format(size / 1000 ** exponent);
    },
  };
}
