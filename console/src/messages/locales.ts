import enGB from "antd/locale/en_GB";
import frCA from "antd/locale/fr_CA";
import zhCN from "antd/locale/zh_CN";
import "dayjs/locale/en-ca";
import "dayjs/locale/fr-ca";
import "dayjs/locale/zh-cn";
import type { MessageLocale } from "./translate";

type AntdLocale = typeof enGB;

export interface SupportedLocale {
  tag: MessageLocale;
  /** The language's name, in the Latin script used by the rest of the code. */
  name: string;
  /** The antd locale object for this tag (antd ships no en_CA). */
  antd: AntdLocale;
  /** The dayjs locale this tag selects. */
  dayjs: string;
  /** The BCP 47 tag handed to Intl. */
  intl: string;
}

/**
 * antd ships no en_CA. en_GB is the base because en_US formats dates as
 * MM/DD; the override points the pickers at the dayjs en-ca locale, which
 * starts the week on Sunday.
 */
const antdEnCA: AntdLocale = { ...enGB, locale: "en-ca" };

/** Every locale the Console knows (ADR 0014); one registry, no ad hoc lists. */
export const SUPPORTED_LOCALES: readonly SupportedLocale[] = [
  {
    tag: "en-CA",
    name: "English",
    antd: antdEnCA,
    dayjs: "en-ca",
    intl: "en-CA",
  },
  { tag: "fr-CA", name: "French", antd: frCA, dayjs: "fr-ca", intl: "fr-CA" },
  { tag: "zh-CN", name: "Chinese", antd: zhCN, dayjs: "zh-cn", intl: "zh-CN" },
];

export const DEFAULT_LOCALE: MessageLocale = "en-CA";

export function isMessageLocale(value: unknown): value is MessageLocale {
  return SUPPORTED_LOCALES.some((locale) => locale.tag === value);
}

export function localeInfo(tag: MessageLocale): SupportedLocale {
  return (
    SUPPORTED_LOCALES.find((locale) => locale.tag === tag) ??
    SUPPORTED_LOCALES[0]
  );
}

/**
 * Maps one `navigator.languages` entry to a supported tag by language subtag:
 * `fr*` to fr-CA (fr-FR readers included), `en*` to en-CA, `zh-CN`, `zh-SG`
 * and `zh-Hans*` to zh-CN. Other `zh-*` tags (zh-TW, zh-HK, zh-Hant*) map to
 * nothing so detection moves on to the next entry.
 */
export function matchLanguage(language: string): MessageLocale | undefined {
  const parts = language.toLowerCase().replace(/_/g, "-").split("-");
  switch (parts[0]) {
    case "fr":
      return "fr-CA";
    case "en":
      return "en-CA";
    case "zh":
      return parts[1] === "cn" || parts[1] === "sg" || parts[1] === "hans"
        ? "zh-CN"
        : undefined;
    default:
      return undefined;
  }
}

/**
 * Detection order: the stored choice, then the first `navigator.languages`
 * entry that matches, then en-CA. A stored tag outside the registry (the
 * retired US English tag, say) is ignored with no alias.
 */
export function detectLocale(
  stored: string | null | undefined,
  languages: readonly string[],
): MessageLocale {
  if (isMessageLocale(stored)) return stored;
  for (const language of languages) {
    const match = matchLanguage(language);
    if (match) return match;
  }
  return DEFAULT_LOCALE;
}
