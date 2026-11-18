import enGB from "antd/locale/en_GB";
import "dayjs/locale/en-ca";
import type { MessageKey } from ".";
import type { MessageLocale } from "./translate";

export type AntdLocale = typeof enGB;

export interface SupportedLocale {
  tag: MessageLocale;
  /** The language's name, in the Latin script used by the rest of the code. */
  name: string;
  /**
   * Loads the antd locale object for this tag (antd ships no en_CA). fr-CA and
   * zh-CN load on demand, with their dayjs locale, so the bundle does not carry
   * all three (ADR 0014); en-CA is the reference and ships with the shell.
   */
  load: () => Promise<AntdLocale>;
  /** The dayjs locale this tag selects. */
  dayjs: string;
  /** The short code the language button shows. */
  code: string;
  /** The BCP 47 tag handed to Intl. */
  intl: string;
  /** The message that names the language in the language picker. */
  labelKey: MessageKey;
  /**
   * Whether the locale ships its own dictionary. Only ready locales are
   * detected from the browser and offered in the picker.
   */
  ready: boolean;
}

/**
 * antd ships no en_CA. en_GB is the base because en_US formats dates as
 * MM/DD; the override points the pickers at the dayjs en-ca locale, which
 * starts the week on Sunday.
 */
export const antdEnCA: AntdLocale = { ...enGB, locale: "en-ca" };

/** Every locale the Console knows (ADR 0014); one registry, no ad hoc lists. */
export const SUPPORTED_LOCALES: readonly SupportedLocale[] = [
  {
    tag: "en-CA",
    name: "English",
    load: async () => antdEnCA,
    dayjs: "en-ca",
    code: "EN",
    intl: "en-CA",
    labelKey: "common.language.en",
    ready: true,
  },
  {
    tag: "fr-CA",
    name: "French",
    load: async () => {
      await import("dayjs/locale/fr-ca");
      // antd names this locale "fr"; the override points its pickers at the
      // dayjs fr-ca locale loaded above.
      return {
        ...(await import("antd/locale/fr_CA")).default,
        locale: "fr-ca",
      };
    },
    dayjs: "fr-ca",
    code: "FR",
    intl: "fr-CA",
    labelKey: "common.language.fr",
    ready: true,
  },
  {
    tag: "zh-CN",
    name: "Chinese",
    load: async () => {
      await import("dayjs/locale/zh-cn");
      return (await import("antd/locale/zh_CN")).default;
    },
    dayjs: "zh-cn",
    code: "ZH",
    intl: "zh-CN",
    labelKey: "common.language.zh",
    ready: true,
  },
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

/** The locales that ship their own dictionary: detected and offered in the picker. */
export const READY_LOCALES: readonly MessageLocale[] = SUPPORTED_LOCALES.filter(
  (locale) => locale.ready,
).map((locale) => locale.tag);

/**
 * Detection order: the stored choice, then the first `navigator.languages`
 * entry that matches, then en-CA. Only `available` locales count (the ready
 * ones by default). A stored tag outside the registry (the retired US English
 * tag, say) is ignored with no alias.
 */
export function detectLocale(
  stored: string | null | undefined,
  languages: readonly string[],
  available: readonly MessageLocale[] = READY_LOCALES,
): MessageLocale {
  if (isMessageLocale(stored) && available.includes(stored)) return stored;
  for (const language of languages) {
    const match = matchLanguage(language);
    if (match && available.includes(match)) return match;
  }
  return DEFAULT_LOCALE;
}
