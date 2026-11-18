import { enCA, type MessageKey } from ".";

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

/**
 * One merged module per locale, fetched on first use so an English reader never
 * downloads the fr-CA or zh-CN text (ADR 0014). en-CA is the reference and
 * ships with the shell.
 */
const loaders: Record<MessageLocale, () => Promise<MessageTable>> = {
  "en-CA": async () => enCA,
  "fr-CA": async () => (await import("./frCA.bundle")).frCA,
  "zh-CN": async () => (await import("./zhCN.bundle")).zhCN,
};

// The tables fetched so far. A key a table lacks, or a locale not fetched yet,
// resolves through the en-CA reference at the end of `resolve`, so a reader
// never sees a blank.
const tables: Partial<Record<MessageLocale, MessageTable>> = {
  "en-CA": enCA,
};
const pending = new Map<MessageLocale, Promise<void>>();

/** Whether the locale's table is in memory, so a translator for it is complete. */
export function isMessagesLoaded(locale: MessageLocale): boolean {
  return tables[locale] !== undefined;
}

/**
 * Fetches the locale's message table (once; concurrent calls share the fetch).
 * A failed fetch rejects and is forgotten, so a later call retries; nothing is
 * half-loaded, so the caller can keep the previous language.
 */
export function loadMessages(locale: MessageLocale): Promise<void> {
  if (isMessagesLoaded(locale)) return Promise.resolve();
  let load = pending.get(locale);
  if (!load) {
    load = loaders[locale]().then(
      (table) => {
        tables[locale] = table;
        pending.delete(locale);
      },
      (error: unknown) => {
        pending.delete(locale);
        throw error;
      },
    );
    pending.set(locale, load);
  }
  return load;
}

function resolve(locale: MessageLocale, key: MessageKey): string {
  for (const tag of [locale, ...LOCALE_FALLBACK[locale]]) {
    const message = tables[tag]?.[key];
    if (message !== undefined) return message;
  }
  return enCA[key];
}

/**
 * A translator bound to one locale; call `loadMessages` first, because a locale
 * whose table is not loaded reads as en-CA. `{name}` placeholders take the matching
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
