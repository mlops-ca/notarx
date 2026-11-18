import type { repositoryEnCA } from "./repository.en-CA";

// fr-CA scaffolding (ADR 0014): empty until the dictionary lands; the total
// Record type and the completeness test come with the strings.
export const repositoryFrCA: Partial<
  Record<keyof typeof repositoryEnCA, string>
> = {};
