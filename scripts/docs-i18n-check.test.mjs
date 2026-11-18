import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import {
  checkFrenchBody,
  coreDocuments,
  discoverEnglishDocuments,
  findPairIssues,
  findSiteMapIssues,
  hasLocalTarget,
  localeTable,
  localePathFor,
  proseLines,
  switcherLocales,
} from './docs-i18n-check.mjs';

// Chinese fixtures are built from code points so no Chinese text is written here.
const chineseBody = '\u4e2d'.repeat(40);

const frenchProse =
  'Ce guide explique comment installer le service et les outils pour vous aider dans votre travail, ' +
  'avec une configuration sans surprise et une sécurité que nous gardons pour chaque dépôt.\n';
const englishProse = 'This guide explains how to install the service and the tools.\n';

const enforcedTable = {
  ...localeTable,
  'fr-CA': { ...localeTable['fr-CA'], enforceRequired: true },
};

function withRoot(fn) {
  const root = mkdtempSync(path.join(tmpdir(), 'notarx-i18n-'));
  try {
    mkdirSync(path.join(root, 'docs'));
    fn(root);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

test('discovers site documents and excludes every localized companion', () => {
  assert.deepEqual(
    discoverEnglishDocuments([
      'README.md',
      'README.zh-CN.md',
      'README.fr-CA.md',
      'docs/guide.md',
      'docs/guide.zh-CN.md',
      'docs/guide.fr-CA.md',
      'internal/note.md',
    ]),
    ['README.md', 'docs/guide.md'],
  );
});

test('accepts Markdown and HTML language links', () => {
  assert.equal(hasLocalTarget('[Francais](README.fr-CA.md)', 'README.fr-CA.md'), true);
  assert.equal(hasLocalTarget('<a href="README.md">English</a>', 'README.md'), true);
  assert.equal(hasLocalTarget('README.md', 'README.md'), false);
});

test('builds companion paths from the locale table', () => {
  assert.equal(localePathFor('docs/guide.md', localeTable['fr-CA']), 'docs/guide.fr-CA.md');
  assert.equal(localePathFor('README.md', localeTable['zh-CN']), 'README.zh-CN.md');
});

test('reports missing Chinese bodies and reciprocal language links', () => {
  withRoot((root) => {
    writeFileSync(path.join(root, 'docs/guide.md'), '# Guide\n');
    writeFileSync(path.join(root, 'docs/guide.zh-CN.md'), `# Guide\n${chineseBody}\n`);
    assert.deepEqual(findPairIssues(root, ['docs/guide.md']), [
      'docs/guide.md: missing language link to guide.zh-CN.md',
      'docs/guide.zh-CN.md: missing language link to guide.md',
    ]);
    writeFileSync(path.join(root, 'docs/guide.zh-CN.md'), '# Guide\n[English](guide.md)\n');
    writeFileSync(path.join(root, 'docs/guide.md'), '# Guide\n[zh](guide.zh-CN.md)\n');
    assert.deepEqual(findPairIssues(root, ['docs/guide.md']), [
      'docs/guide.zh-CN.md: Chinese body is missing or too short',
    ]);
  });
});

test('treats a missing Chinese companion as optional', () => {
  withRoot((root) => {
    writeFileSync(path.join(root, 'docs/guide.md'), '# Guide\n');
    assert.deepEqual(findPairIssues(root, ['docs/guide.md']), []);
  });
});

test('enforces fr-CA core documents only when the locale requires them', () => {
  withRoot((root) => {
    writeFileSync(path.join(root, 'README.md'), '# Notarx\n');
    writeFileSync(path.join(root, 'docs/guide.md'), '# Guide\n');
    assert.deepEqual(findPairIssues(root, ['README.md', 'docs/guide.md']), []);
    assert.deepEqual(
      findPairIssues(root, ['README.md', 'docs/guide.md'], undefined, enforcedTable),
      ['README.fr-CA.md: required fr-CA companion of README.md is missing'],
    );
  });
  assert.deepEqual(coreDocuments, [
    'README.md',
    'docs/README.md',
    'docs/getting-started.md',
    'SECURITY.md',
    'CONTRIBUTING.md',
  ]);
});

test('accepts a real fr-CA companion and rejects a copied one', () => {
  withRoot((root) => {
    writeFileSync(path.join(root, 'docs/guide.md'), `# Guide\n[Francais](guide.fr-CA.md)\n\n${englishProse}`);
    writeFileSync(
      path.join(root, 'docs/guide.fr-CA.md'),
      `# Guide d’installation\n[English](guide.md)\n\n${frenchProse}`,
    );
    assert.deepEqual(findPairIssues(root, ['docs/guide.md']), []);

    writeFileSync(
      path.join(root, 'docs/guide.fr-CA.md'),
      `# Guide\n[English](guide.md)\n\n${englishProse}`,
    );
    const issues = findPairIssues(root, ['docs/guide.md']);
    assert.equal(issues.length, 2);
    assert.match(issues[0], /French body is missing or too short/u);
    assert.match(issues[1], /identical to the English source/u);
  });
});

test('fr-CA identical-line check ignores code, tables and URLs', () => {
  const shared = [
    '```sh',
    'make docs-check',
    'make test',
    '```',
    '| Name | Value |',
    '| --- | --- |',
    'https://example.com/a/b',
    '',
  ].join('\n');
  const english = `${shared}\n${englishProse}`;
  const french = `${shared}\n${frenchProse}${frenchProse.replace('Ce guide', 'Un guide')}`;
  assert.deepEqual(checkFrenchBody('x.fr-CA.md', french, english), []);
  assert.deepEqual(proseLines(shared), []);
  assert.deepEqual(proseLines('See [the docs](https://example.com/x) now'), ['see [the docs] now']);
});

function siteMap(items, overrides = {}) {
  return {
    version: 1,
    defaultLocale: 'en-CA',
    locales: ['en-CA', 'fr-CA', 'zh-CN'],
    sections: [{ id: 'start', title: { en: 'Start' }, items }],
    ...overrides,
  };
}

test('requires the locale table in the site map', () => {
  const guide = { id: 'guide', title: { en: 'Guide' }, en: 'docs/guide.md' };
  assert.deepEqual(findSiteMapIssues(siteMap([guide]), ['docs/guide.md']), []);
  assert.deepEqual(
    findSiteMapIssues(
      siteMap([guide], { defaultLocale: 'en', locales: ['en', 'zh-CN'] }),
      ['docs/guide.md'],
    ),
    [
      'site-map.json: defaultLocale must be en-CA',
      'site-map.json: locales must be [en-CA, fr-CA, zh-CN]',
    ],
  );
});

test('accepts English-only navigation entries and keeps existing companions listed', () => {
  const items = [
    { id: 'guide', title: { en: 'Guide' }, en: 'docs/guide.md' },
    { id: 'old', title: { en: 'Old' }, en: 'docs/old.md' },
  ];
  assert.deepEqual(
    findSiteMapIssues(siteMap(items), ['docs/guide.md', 'docs/old.md'], {
      'zh-CN': new Set(['docs/old.zh-CN.md']),
      'fr-CA': new Set(['docs/guide.fr-CA.md']),
    }),
    [
      'site-map.json: guide must list its existing fr-CA companion docs/guide.fr-CA.md',
      'site-map.json: old must list its existing zh-CN companion docs/old.zh-CN.md',
    ],
  );
});

test('requires fr-CA titles and entries for core items when enforced', () => {
  const readme = { id: 'home', title: { en: 'Home' }, en: 'README.md' };
  assert.deepEqual(findSiteMapIssues(siteMap([readme]), ['README.md']), []);
  assert.deepEqual(findSiteMapIssues(siteMap([readme]), ['README.md'], undefined, enforcedTable), [
    'site-map.json: home must list its required fr-CA companion',
  ]);
  const listed = { ...readme, 'fr-CA': 'README.fr-CA.md' };
  assert.deepEqual(findSiteMapIssues(siteMap([listed]), ['README.md'], undefined, enforcedTable), [
    'site-map.json: home requires a fr-CA title for its fr-CA companion',
  ]);
  const titled = { ...listed, title: { en: 'Home', 'fr-CA': 'Accueil' } };
  assert.deepEqual(findSiteMapIssues(siteMap([titled]), ['README.md'], undefined, enforcedTable), []);
});

test('requires one conventional navigation entry per document pair', () => {
  const guide = {
    id: 'guide',
    title: { en: 'Guide', 'zh-CN': 'x', 'fr-CA': 'Guide' },
    en: 'docs/guide.md',
    'zh-CN': 'docs/guide.zh-CN.md',
    'fr-CA': 'docs/guide.fr-CA.md',
  };
  assert.deepEqual(findSiteMapIssues(siteMap([guide]), ['docs/guide.md']), []);
  assert.deepEqual(findSiteMapIssues(siteMap([guide]), ['docs/guide.md', 'docs/extra.md']), [
    'site-map.json: missing docs/extra.md',
  ]);
  assert.deepEqual(
    findSiteMapIssues(siteMap([{ ...guide, 'fr-CA': 'docs/other.fr-CA.md' }]), ['docs/guide.md']),
    ['site-map.json: guide has a non-conventional fr-CA language pair'],
  );
});

test('language switcher lists only locales that exist for the page', () => {
  assert.deepEqual(switcherLocales({ en: 'docs/a.md' }), ['en-CA']);
  assert.deepEqual(switcherLocales({ en: 'docs/a.md', 'zh-CN': 'docs/a.zh-CN.md' }), ['en-CA', 'zh-CN']);
  assert.deepEqual(
    switcherLocales({ en: 'a.md', 'fr-CA': 'a.fr-CA.md', 'zh-CN': 'a.zh-CN.md' }),
    ['en-CA', 'fr-CA', 'zh-CN'],
  );
});
