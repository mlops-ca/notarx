#!/usr/bin/env node
// GHCR retention for the nightly pipeline (ADR 0024, slice 7): mark and sweep.
//
//   node scripts/prune-ghcr.mjs --owner mlops-ca --packages notarx,notarx-console \
//     --retained-nightlies retained.txt [--owner-type orgs|users] [--apply]
//
// A multi-architecture image is an index plus one untagged manifest per platform
// plus untagged attestation (referrer) manifests, so "delete untagged versions" would
// orphan live images and is never used: nothing here calls a bulk untagged deletion.
//
//   Mark   every tagged version except an expired nightly (tag X.Y.Z-nightly.N that is not
//          in the retained list), then everything reachable from a marked version: the
//          child manifests of an index, and the referrers (attestations, SBOMs) whose
//          subject is a marked digest, including their own children. Stable and
//          mainline versions are therefore always marked.
//   Sweep  versions that are unmarked, older than 7 days (in-flight builds) and older than the
//          oldest retained nightly. Expired nightly indexes go first; their children
//          are orphans and follow in the same ordered pass.
//   Abort  when the mark set is empty (a read failure must never delete everything), or when
//          nightly versions exist but the retained list is empty.
//
// Dry run is the default; --apply deletes through the package versions API.
// Environment for a real or dry run: GH_TOKEN (read:packages, delete:packages) and GH_CLI.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export const NIGHTLY_TAG = /^\d+\.\d+\.\d+-nightly\.\d+$/;
// Referrers fallback tag schema: sha256-<hex> (optionally with a suffix).
const REFERRER_TAG = /^sha256-([0-9a-f]{64})(\..+)?$/;
const DAY_MS = 24 * 60 * 60 * 1000;

const tagsOf = (version) => version.metadata?.container?.tags ?? [];

// versions: [{id, name: "sha256:...", created_at, metadata: {container: {tags}}}]
// manifests: {digest: parsed manifest JSON} (an index lists manifests[], a referrer has subject.digest)
// retainedNightlies: Set of tags without the v prefix.
export function plan({ versions, manifests, retainedNightlies, now = Date.now() }) {
  const byDigest = new Map(versions.map((v) => [v.name, v]));
  const nightlyTagged = versions.some((v) => tagsOf(v).some((t) => NIGHTLY_TAG.test(t)));
  if (nightlyTagged && retainedNightlies.size === 0) {
    throw new Error('nightly versions exist but the retained list is empty; refusing to prune');
  }

  const marked = new Set();
  const queue = [];
  const mark = (digest) => {
    if (byDigest.has(digest) && !marked.has(digest)) {
      marked.add(digest);
      queue.push(digest);
    }
  };

  for (const v of versions) {
    const tags = tagsOf(v);
    const live = tags.some((t) => !NIGHTLY_TAG.test(t) && !REFERRER_TAG.test(t) || retainedNightlies.has(t));
    if (live) mark(v.name);
  }

  // Referrers of a digest: manifests whose subject is that digest, and versions carrying the
  // fallback tag sha256-<hex> of it.
  const referrersOf = new Map();
  const addReferrer = (subject, digest) => {
    if (!referrersOf.has(subject)) referrersOf.set(subject, new Set());
    referrersOf.get(subject).add(digest);
  };
  for (const v of versions) {
    const subject = manifests[v.name]?.subject?.digest;
    if (subject) addReferrer(subject, v.name);
    for (const t of tagsOf(v)) {
      const m = REFERRER_TAG.exec(t);
      if (m) addReferrer('sha256:' + m[1], v.name);
    }
  }

  while (queue.length > 0) {
    const digest = queue.shift();
    for (const child of manifests[digest]?.manifests ?? []) mark(child.digest);
    for (const referrer of referrersOf.get(digest) ?? []) mark(referrer);
  }
  if (marked.size === 0) throw new Error('the mark set is empty; refusing to prune');

  // Oldest retained nightly: the cutoff below which an unmarked version may go.
  const retainedTimes = versions
    .filter((v) => tagsOf(v).some((t) => retainedNightlies.has(t)))
    .map((v) => Date.parse(v.created_at));
  const sevenDays = now - 7 * DAY_MS;
  const cutoff = retainedTimes.length > 0 ? Math.min(sevenDays, ...retainedTimes) : sevenDays;

  const sweep = versions
    .filter((v) => !marked.has(v.name) && Date.parse(v.created_at) < cutoff)
    // Tagged (expired index) versions first, then the untagged orphans.
    .sort((a, b) => Number(tagsOf(a).length === 0) - Number(tagsOf(b).length === 0));
  return { marked, sweep, cutoff };
}

function gh(args, input) {
  return execFileSync(process.env.GH_CLI || 'gh', args, { encoding: 'utf8', input, maxBuffer: 256 * 1024 * 1024 });
}

async function registryToken(owner, name) {
  const user = process.env.GITHUB_ACTOR || owner;
  const headers = process.env.GH_TOKEN ? { Authorization: 'Basic ' + Buffer.from(user + ':' + process.env.GH_TOKEN).toString('base64') } : {};
  const res = await fetch('https://ghcr.io/token?service=ghcr.io&scope=repository:' + owner + '/' + name + ':pull', { headers });
  if (!res.ok) throw new Error('ghcr token request failed: ' + res.status);
  return (await res.json()).token;
}

async function fetchManifest(owner, name, token, digest) {
  const accept = [
    'application/vnd.oci.image.index.v1+json',
    'application/vnd.oci.image.manifest.v1+json',
    'application/vnd.docker.distribution.manifest.list.v2+json',
    'application/vnd.docker.distribution.manifest.v2+json',
  ].join(', ');
  const res = await fetch('https://ghcr.io/v2/' + owner + '/' + name + '/manifests/' + digest, {
    headers: { Authorization: 'Bearer ' + token, Accept: accept },
  });
  if (!res.ok) throw new Error('manifest ' + digest + ' of ' + name + ': ' + res.status);
  return res.json();
}

export function parseArgs(argv) {
  const opts = { ownerType: 'orgs', apply: false };
  for (let i = 0; i < argv.length; i += 1) {
    const flag = argv[i];
    if (flag === '--apply') opts.apply = true;
    else if (['--owner', '--packages', '--retained-nightlies', '--owner-type'].includes(flag)) {
      if (i + 1 >= argv.length) throw new Error(flag + ' needs a value');
      opts[flag.slice(2).replace(/-(\w)/g, (_, c) => c.toUpperCase())] = argv[(i += 1)];
    } else throw new Error('unknown argument ' + flag);
  }
  for (const required of ['owner', 'packages', 'retainedNightlies']) {
    if (!opts[required]) throw new Error('--' + required.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase()) + ' is required');
  }
  if (!['orgs', 'users'].includes(opts.ownerType)) throw new Error('--owner-type must be orgs or users');
  return opts;
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const retained = new Set(
    readFileSync(opts.retainedNightlies, 'utf8').split('\n').map((l) => l.trim().replace(/^v/, '')).filter(Boolean),
  );
  for (const name of opts.packages.split(',')) {
    const base = '/' + opts.ownerType + '/' + opts.owner + '/packages/container/' + name + '/versions';
    const versions = JSON.parse(gh(['api', '--paginate', '--slurp', base + '?per_page=100'])).flat();
    const token = await registryToken(opts.owner, name);
    const manifests = {};
    for (const v of versions) manifests[v.name] = await fetchManifest(opts.owner, name, token, v.name);
    const { marked, sweep, cutoff } = plan({ versions, manifests, retainedNightlies: retained });
    console.log(name + ': ' + versions.length + ' versions, ' + marked.size + ' marked, ' + sweep.length + ' to delete (cutoff ' + new Date(cutoff).toISOString() + ')');
    for (const v of sweep) {
      console.log((opts.apply ? 'delete ' : 'would delete ') + name + ' ' + v.name + ' ' + (tagsOf(v).join(',') || 'untagged'));
      if (opts.apply) gh(['api', '--method', 'DELETE', base + '/' + v.id]);
    }
  }
  if (!opts.apply) console.log('dry run: nothing deleted; pass --apply to delete');
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error('prune-ghcr: ' + error.message);
    process.exit(1);
  });
}
