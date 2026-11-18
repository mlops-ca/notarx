#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const rootDocuments = [
  'README.md',
  'PRODUCT.md',
  'DESIGN.md',
  'CONTEXT.md',
  'ARCHITECTURE.md',
  'CONTRIBUTING.md',
  'SECURITY.md',
  'CHANGELOG.md',
];

// ADR 0014, Documentation policy: English (en-CA spelling) is the only mandatory
// language. Localized companions are driven by this table. `required` lists the
// English documents that must have a companion; `enforceRequired` switches that
// requirement on (fr-CA core documents land in a later slice, issue 37, which
// flips it). Existing companions are always structure-checked.
export const coreDocuments = [
  'README.md',
  'docs/README.md',
  'docs/getting-started.md',
  'SECURITY.md',
  'CONTRIBUTING.md',
];

// The en-CA route is the unsuffixed .md file. Its site-map slot keeps the legacy
// key `en`: renaming it would rewrite every zh-CN title line, and zh-CN content
// is frozen by the owner (2026-11-18).
export const referenceLocale = { tag: 'en-CA', key: 'en' };

export const frenchStopWords = new Set(
  (
    'le la les des du et est un une pour dans que qui avec sur aux ce ces pas ne vous nous ' +
    'sont par ou se sa ses leur plus ont être peut sans ainsi entre chaque'
  ).split(' '),
);
export const minFrenchStopWords = 10;
export const minFrenchStopWordDensity = 0.1;
export const maxIdenticalProseRatio = 0.3;
export const minChineseCharacters = 20;

export const localeTable = {
  'fr-CA': {
    suffix: '.fr-CA.md',
    key: 'fr-CA',
    required: coreDocuments,
    enforceRequired: false,
    checkBody: checkFrenchBody,
  },
  'zh-CN': {
    suffix: '.zh-CN.md',
    key: 'zh-CN',
    required: [],
    enforceRequired: true,
    checkBody: checkChineseBody,
  },
};

export function localePathFor(englishPath, locale) {
  return englishPath.replace(/\.md$/u, locale.suffix);
}

export function discoverEnglishDocuments(markdownFiles, table = localeTable) {
  const suffixes = Object.values(table).map((locale) => locale.suffix);
  return markdownFiles
    .filter(
      (file) =>
        !suffixes.some((suffix) => file.endsWith(suffix)) &&
        (file.startsWith('docs/') || rootDocuments.includes(file)),
    )
    .sort();
}

export function hasLocalTarget(document, target) {
  return (
    document.includes(`](${target})`) ||
    document.includes(`href="${target}"`) ||
    document.includes(`href='${target}'`)
  );
}

// Prose lines exclude fenced code, table rows and URLs, so commands, tables and
// links that legitimately stay identical across locales are not counted.
export function proseLines(document) {
  const lines = [];
  let fence = null;
  for (const raw of document.split('\n')) {
    const trimmed = raw.trim();
    const marker = /^(```|~~~)/u.exec(trimmed);
    if (marker) {
      if (fence === null) fence = marker[1];
      else if (marker[1] === fence) fence = null;
      continue;
    }
    if (fence !== null || trimmed.startsWith('|')) continue;
    const text = trimmed
      .replace(/\]\([^)]*\)/gu, '')
      .replace(/[[\]]/gu, '')
      .replace(/(?:https?|ftp):\/\/\S+/gu, '')
      .replace(/<[^>]*>/gu, '')
      .trim();
    if (/\p{L}/u.test(text)) lines.push(text.toLowerCase());
  }
  return lines;
}

export function checkFrenchBody(frenchPath, french, english) {
  const issues = [];
  const frenchLines = proseLines(french);
  const words = frenchLines.join(' ').match(/\p{L}+/gu) ?? [];
  const hits = words.filter((word) => frenchStopWords.has(word)).length;
  if (hits < minFrenchStopWords || hits / Math.max(words.length, 1) < minFrenchStopWordDensity) {
    issues.push(`${frenchPath}: French body is missing or too short (needs French prose)`);
  }
  const englishLines = new Set(proseLines(english));
  const identical = frenchLines.filter((line) => englishLines.has(line)).length;
  if (frenchLines.length > 0 && identical / frenchLines.length > maxIdenticalProseRatio) {
    issues.push(
      `${frenchPath}: more than ${Math.round(maxIdenticalProseRatio * 100)} percent of prose lines are identical to the English source`,
    );
  }
  return issues;
}

export function checkChineseBody(chinesePath, chinese) {
  const characters = chinese.match(/[\u3400-\u9fff]/gu) ?? [];
  return characters.length < minChineseCharacters
    ? [`${chinesePath}: Chinese body is missing or too short`]
    : [];
}

export function findPairIssues(
  repoRoot,
  englishDocuments,
  readFile = (file) => readFileSync(file, 'utf8'),
  table = localeTable,
) {
  const issues = [];
  for (const englishPath of englishDocuments) {
    for (const [tag, locale] of Object.entries(table)) {
      const localizedPath = localePathFor(englishPath, locale);
      const absoluteEnglish = path.join(repoRoot, englishPath);
      const absoluteLocalized = path.join(repoRoot, localizedPath);
      if (!existsSync(absoluteLocalized)) {
        // A missing companion is silent unless the locale requires it.
        if (locale.enforceRequired && locale.required.includes(englishPath)) {
          issues.push(`${localizedPath}: required ${tag} companion of ${englishPath} is missing`);
        }
        continue;
      }
      const english = readFile(absoluteEnglish);
      const localized = readFile(absoluteLocalized);
      issues.push(...locale.checkBody(localizedPath, localized, english));

      const localizedTarget = path.basename(localizedPath);
      const englishTarget = path.basename(englishPath);
      if (!hasLocalTarget(english, localizedTarget)) {
        issues.push(`${englishPath}: missing language link to ${localizedTarget}`);
      }
      if (!hasLocalTarget(localized, englishTarget)) {
        issues.push(`${localizedPath}: missing language link to ${englishTarget}`);
      }
    }
  }
  return issues;
}

export function flattenSiteMap(siteMap) {
  return siteMap.sections.flatMap((section) => section.items);
}

// The language switcher of a page lists only locales whose companion exists, so
// it is derived from the locales the site-map item actually carries.
export function switcherLocales(item, table = localeTable) {
  return [referenceLocale.tag, ...Object.keys(table).filter((tag) => item[table[tag].key])];
}

// existingByLocale maps a locale tag to the set of companion files on disk.
export function findSiteMapIssues(
  siteMap,
  englishDocuments,
  existingByLocale = undefined,
  table = localeTable,
) {
  const issues = [];
  const expectedLocales = [referenceLocale.tag, ...Object.keys(table)];
  if (siteMap.version !== 1) issues.push('site-map.json: version must be 1');
  if (siteMap.defaultLocale !== referenceLocale.tag) {
    issues.push(`site-map.json: defaultLocale must be ${referenceLocale.tag}`);
  }
  if (siteMap.localeKeys?.[referenceLocale.tag] !== referenceLocale.key) {
    issues.push(`site-map.json: localeKeys must map ${referenceLocale.tag} to ${referenceLocale.key}`);
  }
  if (JSON.stringify(siteMap.locales) !== JSON.stringify(expectedLocales)) {
    issues.push(`site-map.json: locales must be [${expectedLocales.join(', ')}]`);
  }

  const items = flattenSiteMap(siteMap);
  const ids = new Set();
  const englishPaths = new Set();
  const localizedPaths = Object.fromEntries(Object.keys(table).map((tag) => [tag, new Set()]));
  for (const item of items) {
    if (!item.id || ids.has(item.id)) issues.push(`site-map.json: duplicate or empty id ${item.id ?? ''}`);
    ids.add(item.id);
    const referencePath = item[referenceLocale.key];
    if (!item.title?.[referenceLocale.key]) {
      issues.push(`site-map.json: ${item.id} requires an ${referenceLocale.tag} title`);
    }
    if (englishPaths.has(referencePath)) issues.push(`site-map.json: duplicate ${referenceLocale.tag} path ${referencePath}`);
    englishPaths.add(referencePath);

    for (const [tag, locale] of Object.entries(table)) {
      const listed = item[locale.key];
      const mustList = locale.enforceRequired && locale.required.includes(referencePath);
      if (mustList && !listed) {
        issues.push(`site-map.json: ${item.id} must list its required ${tag} companion`);
      }
      if (!listed) continue;
      if (!item.title?.[locale.key]) {
        issues.push(`site-map.json: ${item.id} requires a ${tag} title for its ${tag} companion`);
      }
      if (localizedPaths[tag].has(listed)) issues.push(`site-map.json: duplicate ${tag} path ${listed}`);
      localizedPaths[tag].add(listed);
      if (listed !== localePathFor(referencePath, locale)) {
        issues.push(`site-map.json: ${item.id} has a non-conventional ${tag} language pair`);
      }
    }
  }

  for (const englishPath of englishDocuments) {
    if (!englishPaths.has(englishPath)) issues.push(`site-map.json: missing ${englishPath}`);
  }
  if (existingByLocale) {
    for (const item of items) {
      for (const [tag, locale] of Object.entries(table)) {
        const existing = existingByLocale[tag] ?? new Set();
        const companion = localePathFor(item[referenceLocale.key], locale);
        if (existing.has(companion) && item[locale.key] !== companion) {
          issues.push(`site-map.json: ${item.id} must list its existing ${tag} companion ${companion}`);
        }
        if (item[locale.key] && !existing.has(item[locale.key])) {
          issues.push(`site-map.json: ${item.id} lists missing ${item[locale.key]}`);
        }
      }
    }
  }
  for (const englishPath of englishPaths) {
    if (!englishDocuments.includes(englishPath)) issues.push(`site-map.json: unknown ${englishPath}`);
  }
  return issues;
}

function main() {
  const scriptPath = fileURLToPath(import.meta.url);
  const repoRoot = path.resolve(path.dirname(scriptPath), '..');
  const markdownFiles = execFileSync(
    'git',
    ['ls-files', '--cached', '--others', '--exclude-standard', '--', '*.md'],
    { cwd: repoRoot, encoding: 'utf8' },
  )
    .split('\n')
    .filter((file) => file && existsSync(path.join(repoRoot, file)));
  const englishDocuments = discoverEnglishDocuments(markdownFiles);
  const siteMap = JSON.parse(readFileSync(path.join(repoRoot, 'docs/site-map.json'), 'utf8'));
  const issues = [
    ...findPairIssues(repoRoot, englishDocuments),
    ...findSiteMapIssues(
      siteMap,
      englishDocuments,
      Object.fromEntries(
        Object.entries(localeTable).map(([tag, locale]) => [
          tag,
          new Set(markdownFiles.filter((file) => file.endsWith(locale.suffix))),
        ]),
      ),
    ),
  ];
  if (markdownFiles.some((file) => file.endsWith('.en.md'))) {
    issues.push('English documents must use the unsuffixed .md route');
  }
  if (issues.length > 0) {
    for (const issue of issues) process.stderr.write(`${issue}\n`);
    process.exitCode = 1;
    return;
  }
  process.stdout.write(
    `documentation i18n check passed (${englishDocuments.length} documents, ${siteMap.sections.length} sections)\n`,
  );
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
