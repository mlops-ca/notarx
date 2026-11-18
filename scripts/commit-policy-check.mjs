#!/usr/bin/env node
// Validates a Conventional Commit title against .github/commit-policy.json
// (ADR 0024). The pull request title is the unit of enforcement because the
// squash merge uses it as the commit subject.
//
//   node scripts/commit-policy-check.mjs --title "feat(oci): add range reads"
//   TITLE=... BODY=... node scripts/commit-policy-check.mjs
//   node scripts/commit-policy-check.mjs --warn-range origin/main..HEAD

import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export function loadPolicy(file = path.join(repoRoot, '.github/commit-policy.json')) {
  return JSON.parse(readFileSync(file, 'utf8'));
}

export function allScopes(policy) {
  return Object.values(policy.scopes).flat();
}

const headerPattern = /^([a-z]+)(?:\(([^()]*)\))?(!)?: (.*)$/u;
const breakingFooter = /^BREAKING[ -]CHANGE: \S/mu;

/**
 * @returns {{ok: boolean, errors: string[], type?: string, scopes: string[],
 *   breaking: boolean, releasing: boolean, subject?: string}}
 */
export function checkTitle(title, policy, body = '') {
  const result = { ok: false, errors: [], scopes: [], breaking: false, releasing: false };
  const match = headerPattern.exec(String(title ?? '').trim());
  if (!match) {
    result.errors.push('title must look like "type(scope)!: subject"');
    return result;
  }
  const [, type, scopeList, bang, subject] = match;
  result.type = type;
  result.subject = subject.trim();
  if (!policy.types.includes(type)) {
    result.errors.push(`unknown type "${type}"; allowed: ${policy.types.join(', ')}`);
  }
  if (scopeList !== undefined) {
    const known = new Set(allScopes(policy));
    const scopes = scopeList.split(',').map((scope) => scope.trim());
    result.scopes = scopes;
    if (scopes.some((scope) => scope === '')) {
      result.errors.push('scope list must not be empty or contain empty entries');
    }
    for (const scope of scopes.filter(Boolean)) {
      if (!known.has(scope)) result.errors.push(`unknown scope "${scope}"`);
    }
  }
  if (result.subject === '') result.errors.push('subject must not be empty');
  result.breaking = bang === '!' || breakingFooter.test(String(body ?? ''));
  result.releasing = policy.releasingTypes.includes(type) || result.breaking;
  result.ok = result.errors.length === 0;
  return result;
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const flag = argv[index];
    if (['--title', '--body', '--warn-range', '--policy'].includes(flag)) {
      options[flag.slice(2)] = argv[(index += 1)];
    } else {
      throw new Error(`unknown argument ${flag}`);
    }
  }
  return options;
}

function main() {
  const options = parseArguments(process.argv.slice(2));
  const policy = loadPolicy(options.policy);
  if (options['warn-range']) {
    // Release planning: report nonconforming subjects on main without failing.
    const subjects = execFileSync('git', ['log', '--format=%s', options['warn-range']], {
      cwd: repoRoot,
      encoding: 'utf8',
    })
      .split('\n')
      .filter(Boolean);
    for (const subject of subjects) {
      const result = checkTitle(subject, policy);
      if (!result.ok) process.stdout.write(`::warning::${subject}: ${result.errors.join('; ')}\n`);
    }
    return;
  }
  const title = options.title ?? process.env.TITLE;
  const body = options.body ?? process.env.BODY ?? '';
  const result = checkTitle(title, policy, body);
  if (!result.ok) {
    process.stderr.write(`Title does not follow the commit policy (ADR 0024): ${title}\n`);
    for (const error of result.errors) process.stderr.write(`  - ${error}\n`);
    process.stderr.write('See .github/commit-policy.json for the types and scopes.\n');
    process.exitCode = 1;
    return;
  }
  const kind = result.releasing ? 'releasing' : 'non-releasing';
  process.stdout.write(`Title follows the commit policy (${kind}${result.breaking ? ', breaking' : ''}).\n`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
