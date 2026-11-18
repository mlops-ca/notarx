# Documentation Site Integration Guide

[简体中文](documentation-site-guide.zh-CN.md) | [Documentation index](README.md)

Artifact Gateway keeps its Markdown portable so the repository can later feed
Docusaurus, VitePress, MkDocs, or another static documentation system without
rewriting document ownership and language rules.

## Information architecture

`docs/site-map.json` is the navigation contract (`defaultLocale` is `en-CA`;
`locales` is `["en-CA", "fr-CA", "zh-CN"]`). It defines six ordered
sections, localized section and page titles, stable page IDs, and one en-CA
route for every document plus the fr-CA and zh-CN routes that exist.

The sections are: Start here; Architecture and design; Protocols and formats;
Operations and security; Quality, performance, and release; and Strategy and
reference.

The repository README remains the project landing page. `docs/README.md` and
`docs/README.zh-CN.md` are the documentation landing pages and preserve the
same section order as the site map.

## Locale tiers and route convention

[ADR 0014](adr/0014-locale-policy.md) sets three tiers:

| Locale | Tier | Source path | Route prefix | Requirement |
| --- | --- | --- | --- | --- |
| `en-CA` | reference | unsuffixed `.md` | `/` | every document, blocking |
| `fr-CA` | first-class | `.fr-CA.md` | `/fr-CA/` | core set required, other pages optional |
| `zh-CN` | secondary, community-maintained, frozen at the upstream snapshot | `.zh-CN.md` | `/zh-CN/` | optional; existing files are kept and structure-checked |

- The fr-CA core set is `README`, `docs/README`, `docs/getting-started`,
  `SECURITY` and `CONTRIBUTING`. Other French pages are optional but, when
  present, must link reciprocally and pass the same body checks.
- A missing zh-CN companion is silent. An existing one keeps its reciprocal
  link and its site-map entry.
- Root documents follow the same rule, for example `SECURITY.md` and
  `SECURITY.fr-CA.md`.
- Every localized page links to the English page and to the documentation index.
- `.en.md` is rejected to avoid two English route conventions.
- The site-map slot for the en-CA route and title keeps the key `en`, declared
  in `localeKeys` for adapters; the fr-CA and zh-CN slots use their tags.

**Language switcher.** A page's switcher lists only the locales that exist for
that page: en-CA always, plus each locale whose companion file is listed in the
page's site-map item. The checker requires every existing companion to be
listed and rejects a listed companion that is missing, so a switcher never
links to a page that does not exist. A page without a French or Chinese
companion shows no entry for it.

A site generator may mount en-CA pages at `/`, French pages under
`/fr-CA/` and Chinese pages under `/zh-CN/`, but source paths and IDs
should remain unchanged. Route aliases for
renamed public pages belong in site configuration, not a second source naming scheme.

## Content ownership

The locales are behaviorally equivalent, not necessarily sentence-for-sentence
literal translations. Every locale preserves commands, configuration keys, API routes,
status codes, compatibility limits, security boundaries, evidence scope, and
whether a capability is released, previewed, planned, or historical.

Do not publish a locale stub that only links to the other language. English
(en-CA spelling) is the source: when it changes behaviour, update the fr-CA
companion in the same change when the page is in the core set, and treat
zh-CN as frozen.

## Generator adapter

A future adapter should read `docs/site-map.json` and generate its sidebar or
navigation. It may add generated frontmatter in a build directory, but must not
rewrite source Markdown or commit a second handwritten navigation tree.

Assets remain under `docs/assets/`. Relative links stay valid in the repository
and may be rewritten only in generated output. Root-level pages require the
adapter to preserve their relationship to the documentation tree.

The build should fail when a route is missing, duplicated, not paired, or lacks
a localized title. Search indexes should carry stable page ID, locale, section
ID, title, and public route so language switching does not compare translated titles.

## Local validation

```sh
make docs-check
```

The gate checks tracked and untracked Markdown that exists in the working tree.
It validates local Markdown/HTML links, language-pair naming, reciprocal links,
a locale table that drives the per-locale body checks (French stop-words and at
most 30 percent of prose lines identical to the English source, with fenced
code, tables and URLs excluded; at least 20 Chinese characters for zh-CN),
the fr-CA core set once it is enforced, localized titles, unique IDs and paths, and complete
navigation coverage.

When a page is removed or renamed, update every locale file that exists, inbound links, and
`docs/site-map.json` in the same change. Keep release records under
`docs/release-records/` and ADRs under `docs/adr/`.

## Initial site acceptance

- Both locale homes render all six sections in site-map order.
- Language switching stays on the same stable page ID.
- Root pages, ADRs, release records, diagrams, and images resolve.
- Code blocks, Mermaid, tables, headings, and anchors render correctly.
- Search returns the active locale and records the source locale.
- Redirects cover previously shared documentation URLs.
- CI runs `make docs-check` and the site generator's link/build checks.
