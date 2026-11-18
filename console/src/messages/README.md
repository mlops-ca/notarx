# Console messages

Every piece of display text lives here, one dictionary per feature and locale:
`<feature>.en-CA.ts` is the typed reference and `<feature>.zh-CN.ts` is a lagging,
partial overlay (ADR 0014). `index.ts` merges them into the `enCA` and `zhCN`
tables, and `MessageKey` is the union of every en-CA key. `locales.ts` is the
registry of supported locales (tag, antd locale, dayjs locale, Intl tag) and the
browser-language detection that `usePreferences()` uses.

## Writing a message

Components read the translator from `usePreferences()`:

```tsx
const { t } = usePreferences();
t("repository.deleteRepository", { name: repository.name });
```

- Keys are `<featurePrefix>.<camelCasedEnglish>`; the prefix names the
  dictionary that owns the key (`repository.`, `operations.`, `ui.` for
  `src/components`, `shell.` for `src/app`, `lib.` for `src/lib`).
- The en-CA dictionary defines the key type. A zh-CN dictionary may lack any key
  (the reader then sees the en-CA text, see `LOCALE_FALLBACK` in
  `translate.ts`), but a key that en-CA does not define fails type checking in
  `index.ts`.
- Copy uses Canadian spelling (`colour`, `-ize`). `make spellcheck` runs cspell
  with the en-CA dictionary over these files and the English documentation;
  project terms live in `.cspell/project-words.txt`.
- `{name}` placeholders take the matching variable. Pass whole sentences,
  one key per branch, rather than gluing fragments together, and one key per
  plural form where English needs it.
- Helpers outside React take `t: Translate` as a parameter. Tests build a real
  translator with `createTranslator("en-CA")` from `translate.ts`.
- Code that has no translator (query functions, readers) throws
  `LocalizedError(key, variables)`. `ErrorBanner` and `errorText()` translate
  it where it is shown; its `message` holds the en-CA text for logs.
- Lookup tables store keys (`Record<Code, MessageKey>`), not `[zh, en]` pairs.

## Terms

`glossary.ts` names each domain term from `CONTEXT.md` per locale and lists the
synonyms to avoid.

## Checks

`messages.test.ts` runs with the unit tests and fails when:

- a key is empty or its placeholders differ between locales;
- one key is defined in two dictionaries (the later spread would win);
- a zh-CN key no longer exists in en-CA (`node scripts/i18n-prune.mjs` removes
  them);
- no source file uses a key (keys built at run time from a template prefix,
  such as `account.kind.${kind}`, count as used);
- a message uses a synonym the glossary avoids for its locale;
- a string, template or JSX text outside `src/messages` contains non-Latin
  script, that is, display text that bypassed the dictionaries. Language names
  and the built-in theme package descriptions are exempt.

It also prints the zh-CN coverage percentage, without failing.

## Migrating inline text

`scripts/i18n-migrate.mjs` moved the former `text("zh", "en")` calls and
`[zh, en]` tables into these dictionaries (issue 276). It is kept for
branches that still carry inline calls:

```sh
node scripts/i18n-migrate.mjs            # report only
node scripts/i18n-migrate.mjs --write    # rewrite, then run prettier and tsc
```
