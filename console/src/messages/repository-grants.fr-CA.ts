import type { repositoryGrantsEnCA } from "./repository-grants.en-CA";

// fr-CA scaffolding (ADR 0014): empty until the dictionary lands; the total
// Record type and the completeness test come with the strings.
export const repositoryGrantsFrCA: Partial<
  Record<keyof typeof repositoryGrantsEnCA, string>
> = {};
