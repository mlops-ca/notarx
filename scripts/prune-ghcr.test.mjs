import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { plan, parseArgs } from './prune-ghcr.mjs';

const now = Date.parse('2026-11-18T00:00:00Z');
const day = 24 * 60 * 60 * 1000;
const digest = (c) => 'sha256:' + c.repeat(64);
let nextId = 1;
const version = (name, ageDays, tags = []) => ({
  id: nextId++,
  name,
  created_at: new Date(now - ageDays * day).toISOString(),
  metadata: { container: { tags } },
});
const names = (versions) => versions.map((v) => v.name);

// A multi-architecture index with two platform manifests and an attestation referrer
// that points at the index and carries its own child manifest.
function image(prefix, tag, ageDays) {
  const index = digest(prefix + '1'.slice(0, 0) + 'a');
  const amd = digest(prefix + 'b');
  const arm = digest(prefix + 'c');
  const att = digest(prefix + 'd');
  const attChild = digest(prefix + 'e');
  return {
    versions: [
      version(index, ageDays, [tag]),
      version(amd, ageDays),
      version(arm, ageDays),
      version(att, ageDays),
      version(attChild, ageDays),
    ],
    manifests: {
      [index]: { manifests: [{ digest: amd }, { digest: arm }] },
      [amd]: { layers: [] },
      [arm]: { layers: [] },
      [att]: { subject: { digest: index }, manifests: [{ digest: attChild }] },
      [attChild]: { layers: [] },
    },
    all: [index, amd, arm, att, attChild],
  };
}

function merge(...images) {
  return { versions: images.flatMap((i) => i.versions), manifests: Object.assign({}, ...images.map((i) => i.manifests)) };
}

test('children and attestations of a retained tag are never orphaned', () => {
  const kept = image('1', '0.3.0-nightly.2', 40);
  const result = plan({ ...merge(kept), retainedNightlies: new Set(['0.3.0-nightly.2']), now });
  assert.deepEqual(result.sweep, []);
  assert.equal(result.marked.size, 5);
});

test('an expired nightly index and its children are swept, a retained one is untouched', () => {
  const kept = image('1', '0.3.0-nightly.5', 40);
  const expired = image('2', '0.3.0-nightly.1', 60);
  const result = plan({ ...merge(kept, expired), retainedNightlies: new Set(['0.3.0-nightly.5']), now });
  assert.deepEqual(new Set(names(result.sweep)), new Set(expired.all));
  // The tagged index goes first, the orphans after it.
  assert.equal(result.sweep[0].name, expired.all[0]);
});

test('an index referenced only by a stable tag is marked even when it is old', () => {
  const stable = image('3', '0.3.0', 400);
  const result = plan({ ...merge(stable, image('1', '0.4.0-nightly.1', 2)), retainedNightlies: new Set(['0.4.0-nightly.1']), now });
  assert.deepEqual(result.sweep, []);
});

test('the moving nightly tag keeps its index alive though its versioned tag expired', () => {
  const index = digest('a');
  const child = digest('b');
  const versions = [version(index, 50, ['nightly', '0.3.0-nightly.2']), version(child, 50), version(digest('9'), 3, ['0.4.0-nightly.1'])];
  const manifests = { [index]: { manifests: [{ digest: child }] } };
  const result = plan({ versions, manifests, retainedNightlies: new Set(['0.4.0-nightly.1']), now });
  assert.deepEqual(result.sweep, []);
});

test('versions newer than 7 days or newer than the oldest retained nightly are kept', () => {
  const recent = image('1', '0.3.0-nightly.1', 3);
  const retainedOld = image('2', '0.2.0-nightly.9', 30);
  const between = image('4', '0.2.0-nightly.1', 20);
  const result = plan({ ...merge(recent, retainedOld, between), retainedNightlies: new Set(['0.2.0-nightly.9']), now });
  // recent is expired but younger than 7 days; between is expired and 20 days old, younger than the
  // oldest retained nightly (30 days): both are kept.
  assert.deepEqual(result.sweep, []);
});

test('an unmarked untagged orphan older than the cutoff is swept', () => {
  const kept = image('1', '0.3.0-nightly.2', 40);
  const orphan = version(digest('f'), 90);
  const result = plan({ versions: [...kept.versions, orphan], manifests: kept.manifests, retainedNightlies: new Set(['0.3.0-nightly.2']), now });
  assert.deepEqual(names(result.sweep), [orphan.name]);
});

test('a referrer found only through the sha256-<hex> fallback tag is marked', () => {
  const kept = image('1', '0.3.0-nightly.2', 40);
  const fallback = version(digest('7'), 40, ['sha256-' + kept.all[0].slice(7)]);
  const result = plan({ versions: [...kept.versions, fallback], manifests: kept.manifests, retainedNightlies: new Set(['0.3.0-nightly.2']), now });
  assert.deepEqual(result.sweep, []);
});

test('an empty mark set aborts', () => {
  const expired = image('2', '0.3.0-nightly.1', 60);
  // Only an expired nightly exists and the retained list names something else: nothing is marked.
  assert.throws(() => plan({ ...merge(expired), retainedNightlies: new Set(['0.9.9-nightly.1']), now }), /mark set is empty/);
  assert.throws(() => plan({ versions: [], manifests: {}, retainedNightlies: new Set(), now }), /mark set is empty/);
});

test('nightly versions with an empty retained list abort', () => {
  const expired = image('2', '0.3.0-nightly.1', 60);
  assert.throws(() => plan({ ...merge(expired), retainedNightlies: new Set(), now }), /retained list is empty/);
});

test('arguments are validated and dry run is the default', () => {
  const opts = parseArgs(['--owner', 'o', '--packages', 'a,b', '--retained-nightlies', 'f']);
  assert.equal(opts.apply, false);
  assert.equal(opts.ownerType, 'orgs');
  assert.throws(() => parseArgs(['--owner', 'o']), /--packages is required/);
  assert.throws(() => parseArgs(['--bogus']), /unknown argument/);
  assert.throws(() => parseArgs(['--owner', 'o', '--packages', 'a', '--retained-nightlies', 'f', '--owner-type', 'x']), /orgs or users/);
});

test('the script never calls a bulk untagged deletion', () => {
  const source = readFileSync(new URL('./prune-ghcr.mjs', import.meta.url), 'utf8');
  const code = source.split('\n').filter((line) => !line.trim().startsWith('//')).join('\n');
  assert.doesNotMatch(code, /delete-package-versions|delete-only-untagged|untagged-versions/);
  assert.doesNotMatch(code, /actions\/delete/);
  // The only deletion is one versions/<id> call per swept version.
  assert.equal((code.match(/'DELETE'/g) ?? []).length, 1);
});
