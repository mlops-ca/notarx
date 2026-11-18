import type { nexusBatchEnCA } from "./nexus-batch.en-CA";

// fr-CA scaffolding (ADR 0014): empty until the dictionary lands; the total
// Record type and the completeness test come with the strings.
export const nexusBatchFrCA: Partial<
  Record<keyof typeof nexusBatchEnCA, string>
> = {};
