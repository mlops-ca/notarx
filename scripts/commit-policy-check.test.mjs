import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { allScopes, checkTitle, loadPolicy } from './commit-policy-check.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const policy = loadPolicy();

test('accepts a valid title with and without a scope', () => {
  const scoped = checkTitle('feat(oci): add range reads', policy);
  assert.equal(scoped.ok, true);
  assert.equal(scoped.type, 'feat');
  assert.deepEqual(scoped.scopes, ['oci']);
  assert.equal(scoped.releasing, true);
  assert.equal(checkTitle('docs: describe the runtime roles', policy).ok, true);
  assert.equal(checkTitle('docs: describe the runtime roles', policy).releasing, false);
});

test('rejects an unknown type', () => {
  const result = checkTitle('feature(oci): add range reads', policy);
  assert.equal(result.ok, false);
  assert.match(result.errors[0], /unknown type "feature"/u);
});

test('rejects an unknown scope', () => {
  const result = checkTitle('fix(widgets): handle nil', policy);
  assert.equal(result.ok, false);
  assert.match(result.errors[0], /unknown scope "widgets"/u);
});

test('accepts comma separated scopes and rejects one bad entry', () => {
  const ok = checkTitle('feat(oci,npm): share the digest cache', policy);
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.scopes, ['oci', 'npm']);
  assert.equal(checkTitle('feat(oci, npm): share the digest cache', policy).ok, true);
  assert.equal(checkTitle('feat(oci,widgets): share the digest cache', policy).ok, false);
  assert.equal(checkTitle('feat(oci,): share the digest cache', policy).ok, false);
  assert.equal(checkTitle('feat(): share the digest cache', policy).ok, false);
});

test('a bang marks a breaking change and releases', () => {
  const result = checkTitle('refactor(api)!: rename the repository route', policy);
  assert.equal(result.ok, true);
  assert.equal(result.breaking, true);
  assert.equal(result.releasing, true);
});

test('a BREAKING CHANGE footer marks a breaking change', () => {
  const body = 'Changes the route.\n\nBREAKING CHANGE: the old route is gone';
  const result = checkTitle('docs(api): note the route', policy, body);
  assert.equal(result.ok, true);
  assert.equal(result.breaking, true);
  assert.equal(result.releasing, true);
  assert.equal(checkTitle('docs(api): note the route', policy, 'no footer here').breaking, false);
});

test('rejects an empty or missing subject', () => {
  assert.equal(checkTitle('fix(oci): ', policy).ok, false);
  assert.equal(checkTitle('fix(oci):', policy).ok, false);
  assert.equal(checkTitle('fix:    ', policy).ok, false);
  assert.equal(checkTitle('', policy).ok, false);
  assert.equal(checkTitle('just a sentence', policy).ok, false);
});

test('releasing types are a subset of the types and scopes are unique', () => {
  for (const type of policy.releasingTypes) assert.ok(policy.types.includes(type), type);
  const scopes = allScopes(policy);
  assert.equal(new Set(scopes).size, scopes.length);
});

test('the scopes required by ADRs 0026, 0027 and 0030 exist', () => {
  for (const scope of ['rpm', 'huggingface', 'signer', 'scanner']) {
    assert.ok(allScopes(policy).includes(scope), scope);
  }
});

function directories(parent) {
  const absolute = path.join(repoRoot, parent);
  return readdirSync(absolute)
    .filter((name) => statSync(path.join(absolute, name)).isDirectory())
    .map((name) => `${parent}/${name}`);
}

test('every cmd/* and internal/protocol/* directory has a scope alias', () => {
  const scopes = new Set(allScopes(policy));
  for (const directory of [...directories('cmd'), ...directories('internal/protocol')]) {
    const scope = policy.scopeAliases[directory];
    assert.ok(scope, `${directory} has no scope alias in .github/commit-policy.json`);
    assert.ok(scopes.has(scope), `${directory} aliases unknown scope ${scope}`);
  }
});

test('every scope alias targets a known scope', () => {
  const scopes = new Set(allScopes(policy));
  for (const [directory, scope] of Object.entries(policy.scopeAliases)) {
    assert.ok(scopes.has(scope), `${directory} aliases unknown scope ${scope}`);
  }
});

test('Dependabot commit message prefixes produce valid titles', () => {
  const config = readFileSync(path.join(repoRoot, '.github/dependabot.yml'), 'utf8');
  const prefixes = [...config.matchAll(/^\s*prefix(?:-development)?:\s*"?([^"\n]+?)"?\s*$/gmu)].map(
    (match) => match[1],
  );
  assert.ok(prefixes.length > 0, 'dependabot.yml sets no commit-message prefix');
  for (const prefix of prefixes) {
    const title = `${prefix}: Bump example from 1.0.0 to 1.1.0`;
    assert.equal(checkTitle(title, policy).ok, true, title);
  }
  const updates = [...config.matchAll(/^\s*- package-ecosystem:/gmu)].length;
  const messages = [...config.matchAll(/^\s*commit-message:/gmu)].length;
  assert.equal(messages, updates, 'every update block needs a commit-message prefix');
});

test('the Makefile exposes commit-policy-check', () => {
  assert.match(readFileSync(path.join(repoRoot, 'Makefile'), 'utf8'), /^commit-policy-check:/mu);
});
