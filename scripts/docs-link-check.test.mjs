import assert from 'node:assert/strict';
import {
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import {
  extractDocumentTargets,
  findBrokenDocumentLinks,
  normalizeLocalTarget,
} from './docs-link-check.mjs';

test('extracts Markdown, HTML, image, and reference targets', () => {
  const document = '[guide](docs/guide.md) ![hero](assets/hero.png)\n<a href="README.md">read</a>\n[id]: other.md';
  assert.deepEqual(extractDocumentTargets(document), [
    'docs/guide.md',
    'assets/hero.png',
    'README.md',
    'other.md',
  ]);
});

test('normalizes local targets and skips remote or runtime routes', () => {
  assert.equal(normalizeLocalTarget('guide.md#setup'), 'guide.md');
  assert.equal(normalizeLocalTarget('<folder%20name/file.md>'), 'folder name/file.md');
  assert.equal(normalizeLocalTarget('https://example.test/guide'), null);
  assert.equal(normalizeLocalTarget('/api/v2/repositories'), null);
  assert.equal(normalizeLocalTarget('#setup'), null);
});

test('reports missing and escaping links while accepting valid files', () => {
  const root = mkdtempSync(path.join(tmpdir(), 'notarx-docs-'));
  try {
    mkdirSync(path.join(root, 'docs'));
    writeFileSync(path.join(root, 'README.md'), '[ok](docs/guide.md) [missing](docs/missing.md) [escape](../outside.md)');
    writeFileSync(path.join(root, 'docs/guide.md'), '# Guide\n');
    const files = ['README.md', 'docs/guide.md'];
    const broken = findBrokenDocumentLinks(root, files, (file) =>
      file.endsWith('README.md')
        ? '[ok](docs/guide.md) [missing](docs/missing.md) [escape](../outside.md)'
        : '# Guide\n',
    );
    assert.deepEqual(broken, [
      { file: 'README.md', target: 'docs/missing.md', reason: 'missing target' },
      { file: 'README.md', target: '../outside.md', reason: 'outside repository' },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('tolerates a frozen zh-CN page embedding a renamed image but not a missing page', () => {
  const root = mkdtempSync(path.join(tmpdir(), 'notarx-docs-'));
  try {
    mkdirSync(path.join(root, 'docs'));
    const read = (file) =>
      file.endsWith('docs/index.zh-CN.md') ? '![d](assets/old.png) [p](missing.md)' : '![d](assets/old.png)';
    const broken = findBrokenDocumentLinks(root, ['docs/index.zh-CN.md', 'docs/index.md'], read);
    assert.deepEqual(broken, [
      { file: 'docs/index.zh-CN.md', target: 'missing.md', reason: 'missing target' },
      { file: 'docs/index.md', target: 'assets/old.png', reason: 'missing target' },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('tolerates a zh-CN companion linking to a dropped zh-CN twin', () => {
  const root = mkdtempSync(path.join(tmpdir(), 'notarx-docs-'));
  try {
    mkdirSync(path.join(root, 'docs'));
    const read = (file) =>
      file.endsWith('docs/index.zh-CN.md')
        ? '[a](../CHANGELOG.zh-CN.md) [b](missing.md)'
        : '[a](CHANGELOG.zh-CN.md)';
    const broken = findBrokenDocumentLinks(
      root,
      ['docs/index.zh-CN.md', 'README.md'],
      read,
    );
    assert.deepEqual(broken, [
      { file: 'docs/index.zh-CN.md', target: 'missing.md', reason: 'missing target' },
      { file: 'README.md', target: 'CHANGELOG.zh-CN.md', reason: 'missing target' },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
