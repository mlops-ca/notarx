import type { settingsEnCA } from "./settings.en-CA";

// fr-CA scaffolding (ADR 0014): empty until the dictionary lands; the total
// Record type and the completeness test come with the strings.
export const settingsFrCA: Partial<Record<keyof typeof settingsEnCA, string>> =
  {};
