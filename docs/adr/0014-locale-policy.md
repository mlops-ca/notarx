# Locale Policy: en-CA Reference, fr-CA First-Class, zh-CN Secondary

[Documentation index](../README.md)

Status: accepted (2026-11-17)

## Context

notarx is a hard fork owned by mlops.ca (ADR 0013). Upstream was a Chinese-first
product: Simplified Chinese is the type-defining locale in the Console and every
document had a mandatory `.zh-CN.md` twin. The fork's owner is a Canadian lab
whose own sites ship Canadian English and Canadian French. Facts in the tree:

- **Console dictionaries.** `console/src/messages/` holds 20 feature pairs
  (`<feature>.zh-CN.ts` and `<feature>.en-US.ts`; 4,317 plus 4,130 lines, 3,373 keys).
  `index.ts` spreads the zh-CN files into `zhCN`, and
  `export type MessageKey = keyof typeof zhCN` makes Chinese the key source; the
  English table is `Record<MessageKey, string>`.
- **Translator.** `console/src/messages/translate.ts` declares
  `MessageLocale = "zh-CN" | "en-US"` and picks the table with a ternary.
  `LocalizedError` builds its `message` with `createTranslator("zh-CN")`, so logs
  and tests see Chinese. There is no fallback chain: a missing key is a type error.
- **Locale choice.** `console/src/lib/preferences.tsx:126-128` reads
  `localStorage` and otherwise returns `"zh-CN"`. `console/index.html:2` hardcodes
  `lang="zh-CN"`. `navigator.languages` is not consulted. antd and dayjs locales
  are wired for two locales only (`AntdProvider`).
- **Formatting.** About ten feature files call `toLocaleString` or
  `toLocaleDateString` (for example `console/src/features/operations/HTTPErrorRatePanel.tsx`,
  `console/src/features/system/QuotaAlertsPanel.tsx`) and not all pass the app
  locale, so numbers and dates can disagree with the selected language.
- **Message tests.** `console/src/messages/messages.test.ts` discovers
  dictionaries by the `.zh-CN.ts` suffix, compares key sets and placeholders, and
  rejects Chinese text outside `src/messages` (exempting language names such as
  `简体中文`). `console/src/messages/glossary.ts` maps each term from `CONTEXT.md`
  to a Chinese label and lists Chinese synonyms to avoid; the test enforces them.
  Other unit and e2e tests assert on Chinese strings.
- **Docs.** 101 `.zh-CN.md` files mirror 101 English documents.
  `scripts/docs-i18n-check.mjs` (`rootDocuments`, `chinesePathFor`,
  `findPairIssues`, `findSiteMapIssues`) previously failed when a twin was
  missing. **Already landed in the working tree (step 0 of this ADR, before it is
  accepted):** a missing zh-CN twin is skipped, an existing twin must still have
  at least 20 CJK characters and reciprocal links, `title['zh-CN']` is required
  only for items that list a zh-CN path, and a site-map item must list every
  zh-CN companion that exists on disk. ADRs 0013 onward are English-only, with no
  twin and no language link; ADRs 0001-0012 keep theirs.
  What remains: the script still requires `defaultLocale === 'en'` and
  `locales == ['en','zh-CN']`, has no fr-CA support and no staleness detection,
  and `scripts/docs-capability-check.sh` still lists 21 `.zh-CN.md` files (and
  zh-CN README phrases) as required. It runs via the `test` and `docs-check`
  Makefile targets.
- **Backend locale.** `api/openapi/management/email.yaml` declares
  `locale: {type: string, enum: [en, zh-CN]}` (lines 30, 340, 350, 381).
  `migrations/000136_email_test_delivery.sql` has
  `CHECK (locale IN ('en','zh-CN'))` twice (folded into the baseline by ADR 0015's cut-over).
  `internal/emailnotification/template.go` and `quota.go` branch on those two
  locales, and `internal/agctl/alert_commands.go` (later `internal/notarxctl`, ADR 0013) documents them in flag text.
- **Hard-coded Chinese in the server.** `internal/app/runtime_nodes_api.go:72-90`
  returns issue messages such as `"没有在线 API 节点"` next to codes such as
  `api_unavailable`; `internal/egress/egress.go` also contains Chinese text.
  An English client or a French operator receives Chinese diagnostics.

Reference points outside this repo: `mlops.ca/AGENTS.md` defines the lab's
language conventions (Canadian English with `-ize`, `colour`, `’` and spaced em
dashes; Canadian French with `courriel`, no space before `? ! ;`, a space before
`:`, working jargon kept in English), and its build fails when the `en` and `fr`
key sets differ or a key is unused. ADR 0013 fixes English for issues, commits and
docs; this ADR sets the product-locale rules.

## Considered designs

1. **Keep Chinese as the key source, add French as a third table.** Smallest
   diff, but the reference locale stays a language the owners do not write; new
   keys would need a Chinese string first, and English would remain a
   translation.
2. **Adopt a runtime i18n library (i18next or react-intl) with JSON catalogs.**
   Gains ICU plurals and lazy loading, and costs a dependency, the loss of the
   compile-time `MessageKey` check that the repository already enforces, and a
   rewrite of the typing of about 3,400 keys. The existing translator is 58 lines
   and does what is needed.
3. **Keep the typed TypeScript dictionaries; re-root them on en-CA, make other
   locales typed partial overlays with a fallback chain.** Preserves compile-time
   key safety and the unused-key test, lets zh-CN lag without breaking the
   build, and makes fr-CA complete by test. Chosen.
4. **Machine-translate at runtime.** Rejected: nondeterministic, unreviewable,
   and a data-egress path from an artifact repository.

## Decision

Option 3.

### Locale tiers

| Locale | Tier | Console | Docs | CI |
| --- | --- | --- | --- | --- |
| `en-CA` | reference | complete by construction | complete | blocking |
| `fr-CA` | first-class | 100 percent of keys | core set (below) | blocking for the Console and core docs |
| `zh-CN` | secondary | may lag, falls back to `en-CA` | existing files kept, new ones optional | non-blocking report (existing twins still structure-checked) |

Locale tags are BCP 47 and exact (`en-CA`, `fr-CA`, `zh-CN`). The legacy tags
`en-US` and the bare `en` are retired; there is no alias (consistent with ADR
0013's no-compatibility rule).

### Console message system

- **Files.** Rename `*.en-US.ts` to `*.en-CA.ts` and add `*.fr-CA.ts`;
  `*.zh-CN.ts` stays. Exports become `repositoryEnCA`, `repositoryFrCA`,
  `repositoryZhCN`.
- **Typing.** `index.ts` exports `enCA` (the spread of every `*EnCA`) and
  `MessageKey = keyof typeof enCA`. Each locale file is typed
  `Record<FeatureKey, string>` for `fr-CA` (total) and
  `Partial<Record<FeatureKey, string>>` for `zh-CN`. Excess keys fail type
  checking in both, so removing or renaming an en-CA key requires the same change
  in zh-CN in the same PR; `scripts/i18n-prune.mjs` (new, slice 2) does this
  mechanically. Adding keys never touches zh-CN.
- **Fallback.** `createTranslator(locale)` resolves
  `table[locale][key] ?? table["en-CA"][key]` for `zh-CN`; `fr-CA` has no runtime
  fallback because completeness is a test. `LOCALE_FALLBACK:
  Record<MessageLocale, readonly MessageLocale[]>` is the single source of the
  chain, so a later `fr-FR` could fall back through `fr-CA`.
- **Registry.** One `locales.ts` exports
  `SUPPORTED_LOCALES = [{tag:"en-CA", name:"English", antd, dayjs, intl}, ...]`
  (the `antd` and `dayjs` fields are chosen per tag, not derived from it);
  `PreferenceControls.tsx`, `CommandPalette.tsx` and `AntdProvider` iterate it
  instead of listing locales. Native language names are exempt from the
  no-foreign-script rule.
- **Detection (`preferences.tsx`).** Order: stored choice in `localStorage` (key
  unchanged), then the first match of `navigator.languages` by language subtag
  (`fr*` to `fr-CA`, deliberately including `fr-FR` readers; `en*` to `en-CA`;
  `zh-CN`, `zh-SG` and `zh-Hans*` to `zh-CN`). Other `zh-*` tags (`zh-TW`,
  `zh-HK`, `zh-Hant*`) are skipped and fall through to the next entry of
  `navigator.languages`, then `en-CA`.
  `document.documentElement.lang` is set from the active tag; `index.html` ships
  `lang="en-CA"`.
- **Errors.** `LocalizedError.message` is built from the `en-CA` table so logs and
  test output are English; `errorText` translates by key as today.
- **Formatting.** Introduce `useFormatters()` (`console/src/lib/format.ts`) that
  returns `Intl.NumberFormat`, `DateTimeFormat`, `RelativeTimeFormat` and
  `ListFormat` bound to the active tag. A lint rule (`no-restricted-syntax` on
  `.toLocaleString(` and `.toLocaleDateString(` without a locale argument)
  replaces ad hoc calls. Byte sizes keep SI units; the decimal separator and the
  narrow no-break space before units come from `Intl` (`1,5 Go` in fr-CA).
  Plurals use `Intl.PluralRules`: one key
  per plural form, and `fr-CA` needs the `one` form to cover 0 and 1.
- **antd and dayjs.** antd ships no `en_CA` locale (its list has `en_GB`,
  `en_US`, `fr_CA`, `zh_CN`). Mapping: `en-CA` to `antd/locale/en_GB` with
  `ConfigProvider` overrides for any en-CA differences (`en_US` is not used
  because its date formats are MM/DD), `fr-CA` to `antd/locale/fr_CA`, `zh-CN`
  to `antd/locale/zh_CN`. dayjs uses `dayjs/locale/en-ca`, `fr-ca` and `zh-cn`
  (slice 1 verifies the files exist once console dependencies are installed),
  loaded lazily per locale so the bundle does not carry all three.

### fr-CA writing rules

Taken from `mlops.ca/AGENTS.md` and the Office québécois de la langue française
usage:

- `courriel`, not `e-mail` or `email`, in prose; the `email` feature name stays
  in code identifiers and API paths.
- No space before `?`, `!` or `;`; a no-break space before `:`; guillemets
  `« »` with no-break spaces inside; the typographic apostrophe `’`.
- Keep working jargon in English: pipeline, serving, edge, token, namespace,
  webhook, runner, manifest, digest, tag.
- Sentence-case headings and button labels; imperative for actions ("Supprimer
  le dépôt"); inclusive wording with doublets only where unavoidable.
- Unit and number formats come from `Intl`, never from message text.

The `messages.test.ts` fr-CA check also rejects `e-mail`, `email` (outside
placeholders and code spans), a regular space before `:`, `?`, `!`, `;`, and
straight apostrophes between letters.

### Glossary

`glossary.ts` is restructured from `{en, zh, avoid}` to
`{term, labels: {"en-CA": string, "fr-CA": string, "zh-CN"?: string},
avoid?: {"fr-CA"?: string[], "zh-CN"?: string[]}}`, keyed on the `CONTEXT.md`
term. Initial fr-CA labels (reviewed by a fluent speaker before merge):
Repository, dépôt; Artifact, artefact; Promotion, promotion; Group, groupe;
Tombstone, pierre tombale (avoid *épitaphe*); Quarantine, quarantaine; Upstream,
amont; Proxy repository, dépôt mandataire (the word *proxy* is kept in the
interface labels, as jargon); Service account, compte de service; Grant,
autorisation (avoid *subvention*); Audit event, événement d’audit. The glossary
test enforces the avoid lists per locale.

### Test migration

- Unit, e2e and visual tests assert on `en-CA` text, preferably through
  `createTranslator("en-CA")` so a copy change does not break them. Tests that
  need a second language use `fr-CA`.
- The Chinese-literal assertions are replaced mechanically in one slice
  (`rg -l "[一-龥]" console/src console/e2e` is the work list) and the
  no-Chinese-outside-`src/messages` rule becomes a no-non-Latin-outside-messages
  rule with the language-name exemption.
- `messages.test.ts` discovers dictionaries by `.en-CA.ts`; asserts fr-CA key
  set equals en-CA; asserts zh-CN keys are a subset; checks placeholders for
  every locale present; and prints the zh-CN coverage percentage without failing.
- A Playwright smoke test loads the Console with `Accept-Language: fr-CA` and
  with `fr-CA` in `navigator.languages`, and asserts `lang="fr-CA"`, a French
  heading, and French date formatting.

### Documentation policy

- English (`en-CA` spelling) is the only mandatory documentation language.
  Step 0 (landed) made zh-CN twins optional; slice 7 generalises
  `scripts/docs-i18n-check.mjs` to a locale table
  `{ "fr-CA": {suffix: ".fr-CA.md", required: core}, "zh-CN": {suffix:
  ".zh-CN.md", required: none} }`.
- **fr-CA core set (required, CI-blocking):** `README`, `docs/README`,
  `docs/getting-started` (which carries installation), `SECURITY` and
  `CONTRIBUTING`. Everything else in French is optional but, when present, must
  link reciprocally and meet the length check.
- **zh-CN:** existing twins stay and are checked for structure only when present.
  A missing twin is silent (landed). A stale twin warning is a slice 7 option
  implemented by comparing the English file's last git commit to the twin's
  (warning only); if that proves noisy it is dropped and this ADR is amended.
  English documents no longer require a Chinese link, but a present twin keeps
  its reciprocal link and must be listed in `site-map.json`.
- `docs/site-map.json` declares `locales: ["en-CA","fr-CA","zh-CN"]`,
  `defaultLocale: "en-CA"` (the checker's hard-coded `en` and `[en, zh-CN]`
  expectations are replaced; the unsuffixed `.md` route stays the English one); `title` needs `en-CA` for every item, `fr-CA` for
  core items, and zh-CN is optional. The language switcher lists only locales
  that exist for the current page. Route prefixes are `/fr-CA/` and `/zh-CN/`.
- The CJK body check generalises to a per-locale sanity check (fr-CA body must
  contain French stop-words and no more than 30 percent of prose lines identical to
  the English source, with fenced code, tables and URLs excluded), which catches
  copied files.

### Backend locale

- `api/openapi/management/email.yaml`: `locale` becomes
  `enum: [en-CA, fr-CA, zh-CN]` (the four places listed above), regenerate Go and
  TypeScript clients.
- A new numbered migration (`000002` or the next free number after ADR 0015's
  cut-over; baselines are never edited in place) replaces the two
  `CHECK (locale IN ('en','zh-CN'))` constraints with
  `CHECK (locale IN ('en-CA','fr-CA','zh-CN'))`, default `'en-CA'`. It rewrites
  existing rows, so it carries an `UPDATE` mapping `en` to `en-CA`. The
  re-squash immediately before v0.1.0 (ADR 0015) folds it into the fresh baseline.
- `internal/emailnotification/template.go` and `quota.go` (slice 7) move text into
  `internal/emailnotification/locales/<tag>.json` bundles embedded with
  `go:embed`, rendered by the existing `html/template` files. A missing zh-CN
  string falls back to en-CA; fr-CA completeness is a Go test. The default for
  new targets is `en-CA` and `NOTARX_EMAIL_DEFAULT_LOCALE` overrides it (slice 7 adds the key; ADR 0013's environment table lists it).
- `internal/notarxctl/alert_commands.go` (renamed from `internal/agctl` by ADR 0013) flag help lists the three tags.
- **Server diagnostics carry codes, not prose.** `runtime_nodes_api.go` already
  has stable codes (`api_unavailable`, `scheduler_unavailable`,
  `worker_unavailable`, `duplicate_instance_id`, `stale_nodes`). The response
  keeps `code` and `severity` and an English `message`; the Console maps `code` to
  a localized message key and falls back to the English text for unknown codes.
  The same rule applies to `internal/egress/egress.go` and to any new error
  response: English `message`, stable `code`, no localized strings from the
  server except email bodies. `Accept-Language` is not interpreted by the API.
- CLI output (`notarxctl`) is English; the CLI does not localize.

### Translation workflow

1. A change adds or edits `en-CA` strings (and fr-CA in the same PR when the
   change owner can; otherwise the PR adds the keys to fr-CA with a
   `// TODO(fr-CA)` marker holding the en-CA text, recorded as `draft` in the
   sidecar; the completeness test accepts it, and `make i18n-release-check` fails
   on any `draft` key).
2. `scripts/i18n-draft.mjs` (new) produces draft fr-CA and zh-CN strings for
   missing keys with an LLM run by the contributor locally, always including the
   glossary and writing rules in the prompt. Drafts are marked in a sidecar
   `console/src/messages/.review/fr-CA.json` (`{key: "draft"|"reviewed"}`; `critical` is a separate flag).
3. A fluent human reviews fr-CA drafts; CODEOWNERS routes `*.fr-CA.ts` and
   `*.fr-CA.md` to the fr-CA reviewer group. A release candidate fails
   `make i18n-release-check` while any fr-CA key is `draft`.
4. zh-CN has no release gate; community or maintainer contributions are welcome
   and are not blocked by review of other locales.

## Security considerations

- Locale values reach HTML (`lang`), email subjects and a CHECK constraint;
  the registry is a closed set, so tags are matched against it and never
  interpolated from request input.
- Placeholders are substituted as text; email templates use `html/template`, so
  a repository name containing markup is escaped in every locale. Add a test with
  `<script>` in a placeholder for each bundle.
- Machine-drafted strings are reviewed before release to avoid misleading
  wording on destructive actions (delete, quarantine, revoke); those keys are
  tagged `critical` in the review sidecar and require human review even for zh-CN.
- Draft tooling sends only UI strings, never repository data.

## Consequences

- English becomes the key source and the default; a French browser gets French
  with no setting, which matches the owner's Canadian user base.
- fr-CA adds about 3,400 strings (20 files) and a recurring review cost; the
  completeness test makes that cost visible per PR instead of at release.
- zh-CN contributors lose the guarantee that every English change is mirrored;
  readers see English for untranslated keys rather than a build break.
- Existing Chinese-asserting tests and the glossary change shape; the
  one-time churn is large but mechanical.
- The wire locale values change (`en` to `en-CA`), which is a breaking API
  change; acceptable because 0.1.0 restarts versioning (ADR 0013) and ships
  before any external consumer exists (v0.1.0 is the first release, ADR 0013).
- Server diagnostics become locale-neutral, so third-party API consumers get
  stable codes.

## Implementation slices

Release scope: this ADR is part of the v0.1.0 foundation set (ADRs 0013-0017).
In the global order of ADR 0013 it runs after ADR 0013 slices 1-8 and 11 and
after ADR 0015's cut-over (slice 4), in parallel with ADRs 0016 and 0017, and before the
re-squash and the v0.1.0 tag. Step 0 (zh-CN twins optional in
`scripts/docs-i18n-check.mjs`, `docs/site-map.json`) is already landed in the
working tree and needs no slice.

1. **Reference-locale reroot.** Scope: `console/src/messages/*` (rename
   `*.en-US.ts` to `*.en-CA.ts`, re-type zh-CN as partial), `index.ts`,
   `translate.ts`, `locales.ts`, `preferences.tsx`, `AntdProvider` (en_GB/fr_CA/zh_CN
   mapping), `index.html`. Acceptance:
   - [ ] `MessageKey` derives from `enCA`; zh-CN files are `Partial`.
   - [ ] Fallback chain and `LocalizedError` use en-CA; unit tests cover both.
   - [ ] Detection test covers stored choice, `navigator.languages` (`fr`, `en`,
     `zh-CN`, `zh-Hans`, `zh-TW` skipped, unknown) and default.
   - [ ] `make console-test`, `make lint` pass; no `en-US` string remains
     (`rg "en-US" console/src` is empty).
   Dependencies: ADR 0013 slice 3.
2. **Test migration off Chinese.** Scope: console unit, e2e and visual tests,
   `messages.test.ts`, `glossary.ts` restructure, `scripts/i18n-prune.mjs`.
   Acceptance:
   - [ ] No Chinese literal in tests outside zh-CN-specific cases.
   - [ ] Per-locale glossary and placeholder checks in place; zh coverage printed.
   - [ ] Stale zh-CN keys are detected and `i18n-prune.mjs` removes them.
   - [ ] `make console-test` and the e2e target pass.
   Dependencies: slice 1.
3. **Intl formatting.** Scope: `console/src/lib/format.ts`, the 11 files calling
   `toLocaleString`/`toLocaleDateString`, ESLint config. Acceptance:
   - [ ] `useFormatters()` with unit tests per locale (number, date, list, bytes).
   - [ ] Lint rule forbids locale-less calls; all call sites converted.
   - [ ] `make lint`, `make console-test`.
   Dependencies: slice 1.
4. **fr-CA Console, infrastructure.** Scope: `PreferenceControls.tsx`,
   `CommandPalette.tsx`, registry entry, lazy dayjs, fr-CA writing-rule checks in
   `messages.test.ts`, empty-allowed fr-CA scaffolding. Acceptance:
   - [ ] fr-CA is selectable and the typography checks run.
   - [ ] `make console-test`.
   Dependencies: slices 1-3.
5. **fr-CA Console dictionaries (three PRs of about seven feature files each).**
   Scope: `*.fr-CA.ts`, sidecar entries, glossary labels. Acceptance per PR:
   - [ ] Files added; typed total `Record`; typography and glossary checks pass.
   - [ ] Strings marked `draft` in `.review/fr-CA.json`.
   - [ ] `make console-test`.
   The last PR also adds the Playwright smoke test (`Accept-Language: fr-CA`,
   `lang="fr-CA"`, French heading, French date) and requires the fr-CA key set to
   equal en-CA. Dependencies: slice 4.
6. **Backend locale contract and migration.** Scope:
   `api/openapi/management/email.yaml`, regenerated `internal/admin/openapi` and
   the TS client, the new numbered migration, `EmailTargetEditor.tsx`,
   `EmailTemplatePreview.tsx`, `internal/notarxctl/alert_commands.go`.
   Acceptance:
   - [ ] `make openapi-check` and `make test` pass; coverage floors hold.
   - [ ] Migration test: `en` rows become `en-CA`; constraint rejects `en`,
     `en-US`.
   Dependencies: ADR 0015 slice 4 (cut-over), ADR 0013 slice 3.
7. **Email locale bundles.** Scope: `internal/emailnotification/` (`locales/<tag>.json`,
   `template.go`, `quota.go`), `NOTARX_EMAIL_DEFAULT_LOCALE` (also registered in
   ADR 0013's env table). Acceptance:
   - [ ] Template tests for all three locales, zh-CN fallback, fr-CA completeness,
     and HTML escaping with `<script>` placeholders.
   - [ ] `make test`.
   Dependencies: slice 6.
8. **Locale-neutral server diagnostics.** Scope: `internal/app/runtime_nodes_api.go`,
   `internal/egress/egress.go`, OpenAPI code enums, Console code-to-key maps,
   fr-CA/zh-CN strings. Acceptance:
   - [ ] No CJK in non-test Go files (`rg "[一-龥]" internal --glob '!*_test.go'`
     empty; CI grep added to `make architecture-check` or the lint script).
   - [ ] Existing Go tests updated; `make test`, `make openapi-check`.
   Dependencies: slice 1.
9. **Docs checker locale table.** Scope: `scripts/docs-i18n-check.mjs` and its
   test, `scripts/docs-capability-check.sh` (drop the required zh-CN files and
   phrases), `docs/site-map.json` (`locales`, `defaultLocale`). Acceptance:
   - [ ] Locale table drives the checker; fr-CA core files enforced; stop-word and
     identical-line checks exclude code, tables and URLs.
   - [ ] Optional stale-twin warning (git commit comparison), or a documented drop.
   - [ ] `node --test scripts/docs-i18n-check.test.mjs` and `make docs-check`.
   Dependencies: ADR 0013 slice 3.
10. **Docs language switchers and guides.** Scope: `docs/documentation-site-guide.md`,
    `CONTRIBUTING.md`, `PRODUCT.md`, route prefixes. Acceptance:
    - [ ] Switcher lists only existing locales; guides describe the tiers.
    - [ ] `make docs-check`.
    Dependencies: slice 9.
11. **fr-CA core docs.** Scope: `README.fr-CA.md`, `docs/README.fr-CA.md`,
    `docs/getting-started.fr-CA.md`, `SECURITY.fr-CA.md`,
    `CONTRIBUTING.fr-CA.md`. Acceptance:
    - [ ] Reciprocal links; reviewed by the fr-CA group; `make docs-check`.
    Dependencies: slice 9.
12. **Translation tooling and gate.** Scope: `scripts/i18n-draft.mjs`,
    `.review/fr-CA.json`, CODEOWNERS, `make i18n-release-check`. Acceptance:
    - [ ] Draft script is deterministic given a stub model in tests.
    - [ ] Release check fails on `draft` keys and passes when reviewed.
    Dependencies: slice 5.

## Open questions

- Who is the fr-CA reviewer group, and is review of `critical` keys required
  from a Québec-based speaker (terminology such as *dépôt* versus *référentiel*)?
- Should `en-CA` spelling (`colour`, `-ize`) apply to the Console, or should
  product copy stay spelling-neutral to ease cherry-picks from upstream (ADR
  0013)? This ADR assumes en-CA spelling and accepts the cost in cherry-picked
  copy.
- Is a zh-CN maintainer identified, or is zh-CN frozen at the upstream snapshot
  until a contributor appears?
