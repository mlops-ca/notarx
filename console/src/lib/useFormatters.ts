import { useMemo } from "react";
import { localeInfo } from "../messages/locales";
import { createFormatters, type Formatters } from "./format";
import { usePreferences } from "./preferences";

/**
 * The Intl formatters (number, date and time, relative time, list, plural
 * rules, byte sizes) bound to the active Console locale, so numbers and dates
 * agree with the selected language (ADR 0014). Call this instead of
 * `toLocaleString()`, which the lint rule forbids without a locale.
 */
export function useFormatters(): Formatters {
  const { locale } = usePreferences();
  return useMemo(() => createFormatters(localeInfo(locale).intl), [locale]);
}
