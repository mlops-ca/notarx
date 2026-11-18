import type { auditEnCA } from "./audit.en-CA";

// fr-CA scaffolding (ADR 0014): empty until the dictionary lands; the total
// Record type and the completeness test come with the strings.
export const auditFrCA: Partial<Record<keyof typeof auditEnCA, string>> = {};
