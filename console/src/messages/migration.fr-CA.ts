import type { migrationEnCA } from "./migration.en-CA";

// fr-CA scaffolding (ADR 0014): empty until the dictionary lands; the total
// Record type and the completeness test come with the strings.
export const migrationFrCA: Partial<
  Record<keyof typeof migrationEnCA, string>
> = {};
