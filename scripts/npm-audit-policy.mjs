export function auditFailures(report, accepts) {
  if (!report || report.error || !report.vulnerabilities || typeof report.vulnerabilities !== 'object' || Array.isArray(report.vulnerabilities) || !report.metadata?.vulnerabilities) throw new Error('npm audit returned an invalid or failed report');
  const nodes = report.vulnerabilities;
  const counts = report.metadata.vulnerabilities;
  const severities = ['info', 'low', 'moderate', 'high', 'critical'];
  if ([...severities, 'total'].some(key => !Number.isSafeInteger(counts[key]) || counts[key] < 0) || severities.reduce((sum, key) => sum + counts[key], 0) !== counts.total || counts.total !== Object.keys(nodes).length) throw new Error('npm audit returned inconsistent vulnerability counts');
  // A node is acceptable when every advisory reachable through its via graph
  // is accepted. Back edges (npm reports peer-dependency cycles such as
  // vitest <-> @vitest/coverage-v8) add no advisory of their own; a node that
  // reaches no advisory, or an unknown node, still fails closed.
  function visit(name) {
    const seen = new Set();
    const advisories = [];
    let valid = true;
    (function walk(current) {
      if (seen.has(current)) return;
      seen.add(current);
      const node = nodes[current];
      if (!node || !Array.isArray(node.via) || node.via.length === 0) { valid = false; return; }
      for (const item of node.via) {
        if (typeof item === 'string') walk(item);
        else if (item && typeof item.url === 'string') advisories.push([item, current]);
        else valid = false;
      }
    })(name);
    return valid && advisories.length > 0 && advisories.every(([item, owner]) => accepts(item, owner));
  }
  return Object.keys(nodes).filter(name => !visit(name));
}

export function parseAuditResult(result) {
  if (result.error || result.signal || ![0, 1].includes(result.status)) throw result.error ?? new Error('npm audit failed to execute');
  let report;
  try { report = JSON.parse(result.stdout); }
  catch { throw new Error('npm audit returned invalid JSON'); }
  auditFailures(report, () => false);
  if ((result.status === 0) !== (report.metadata.vulnerabilities.total === 0)) throw new Error('npm audit exit status does not match report');
  return report;
}

export function acceptsAdvisory(advisory, name, now) {
  if (advisory.url === 'https://github.com/advisories/GHSA-qwww-vcr4-c8h2' && name === 'react-router') {
    if (now >= new Date('2026-11-01T00:00:00Z')) throw new Error('react-router audit exception expired');
    return 'Client-only Vite SPA does not enable React Router RSC actions; expires 2026-11-01.';
  }
  if (advisory.url === 'https://github.com/advisories/GHSA-fc5q-39xc-vjpf' && name === 'picomatch') {
    if (now >= new Date('2026-12-17T00:00:00Z')) throw new Error('picomatch audit exception expired');
    return 'No fixed picomatch release exists (4.0.7 is latest); it is reachable only through Vite/Vitest dev tooling matching trusted local globs and is not shipped in the Console bundle; expires 2026-12-17.';
  }
  return false;
}
