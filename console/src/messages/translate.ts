import { enCA, zhCN, type MessageKey } from ".";

export type MessageLocale = "en-CA" | "fr-CA" | "zh-CN";

export type MessageVariables = Record<string, string | number>;

/**
 * The single source of the fallback chain (ADR 0014). zh-CN is a lagging
 * partial overlay and falls back to en-CA; fr-CA has no runtime fallback
 * because its completeness is a test, not a runtime concern.
 */
export const LOCALE_FALLBACK: Record<MessageLocale, readonly MessageLocale[]> =
  {
    "en-CA": [],
    "fr-CA": [],
    "zh-CN": ["en-CA"],
  };

type MessageTable = Partial<Record<MessageKey, string>>;

// fr-CA ships in a later slice; until its dictionaries land it serves the
// en-CA reference so a French reader gets readable English, not blanks.
const tables: Record<MessageLocale, MessageTable> = {
  "en-CA": enCA,
  "fr-CA": enCA,
  "zh-CN": zhCN,
};

function resolve(locale: MessageLocale, key: MessageKey): string {
  for (const tag of [locale, ...LOCALE_FALLBACK[locale]]) {
    const message = tables[tag][key];
    if (message !== undefined) return message;
  }
  return enCA[key];
}

/**
 * A translator bound to one locale. `{name}` placeholders take the matching
 * variable; a placeholder without one stays literal, and inserted values are
 * never matched again.
 */
export function createTranslator(locale: MessageLocale) {
  return (key: MessageKey, variables?: MessageVariables): string => {
    const message = resolve(locale, key);
    if (!variables) return message;
    return message.replace(/\{([^{}]+)\}/g, (placeholder, name: string) =>
      Object.hasOwn(variables, name) ? String(variables[name]) : placeholder,
    );
  };
}

export type Translator = ReturnType<typeof createTranslator>;

/**
 * A failure whose text is a message key, so code without a translator (query
 * functions, readers) can still fail in the reader's language. Whatever shows
 * the error translates it with `errorText`; `message` holds the en-CA text.
 */
export class LocalizedError extends Error {
  readonly key: MessageKey;
  readonly variables?: MessageVariables;
  readonly status?: number;

  constructor(
    key: MessageKey,
    variables?: MessageVariables,
    options?: { status?: number },
  ) {
    // The message is readable where nothing translates it (logs, tests).
    super(createTranslator("en-CA")(key, variables));
    this.name = "LocalizedError";
    this.key = key;
    this.variables = variables;
    this.status = options?.status;
  }
}

/** An error's text in the reader's language, else the fallback message. */
export function errorText(
  error: unknown,
  t: Translator,
  fallback: MessageKey,
): string {
  if (error instanceof LocalizedError) return t(error.key, error.variables);
  if (error instanceof Error && error.message) return error.message;
  return t(fallback);
}
