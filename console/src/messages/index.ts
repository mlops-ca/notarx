import { uiEnCA } from "./ui.en-CA";
import { libEnCA } from "./lib.en-CA";
import { formatsEnCA } from "./formats.en-CA";
import { artifactDetailEnCA } from "./artifact-detail.en-CA";
import { repositoryEnCA } from "./repository.en-CA";
import { auditEnCA } from "./audit.en-CA";
import { dashboardEnCA } from "./dashboard.en-CA";
import { searchEnCA } from "./search.en-CA";
import { operationsEnCA } from "./operations.en-CA";
import { migrationEnCA } from "./migration.en-CA";
import { nexusBatchEnCA } from "./nexus-batch.en-CA";
import { nexusScheduleEnCA } from "./nexus-schedule.en-CA";
import { systemEnCA } from "./system.en-CA";
import { groupsEnCA } from "./groups.en-CA";
import { settingsEnCA } from "./settings.en-CA";
import { repositoryGrantsEnCA } from "./repository-grants.en-CA";
import { accessEnCA } from "./access.en-CA";
import { identityEnCA } from "./identity.en-CA";
import { publicBrowseEnCA } from "./public-browse.en-CA";
import { shellEnCA } from "./shell.en-CA";
import { workloadEnCA } from "./workload.en-CA";
import { assertionsEnCA } from "./assertions.en-CA";

/** The typed reference: every Console message, merged from the feature dictionaries (ADR 0014). */
export const enCA = {
  ...uiEnCA,
  ...libEnCA,
  ...formatsEnCA,
  ...artifactDetailEnCA,
  ...repositoryEnCA,
  ...auditEnCA,
  ...dashboardEnCA,
  ...searchEnCA,
  ...operationsEnCA,
  ...migrationEnCA,
  ...nexusBatchEnCA,
  ...nexusScheduleEnCA,
  ...systemEnCA,
  ...groupsEnCA,
  ...settingsEnCA,
  ...repositoryGrantsEnCA,
  ...accessEnCA,
  ...identityEnCA,
  ...publicBrowseEnCA,
  ...shellEnCA,
  ...workloadEnCA,
  ...assertionsEnCA,
};

export type MessageKey = keyof typeof enCA;

// The fr-CA and zh-CN tables are separate chunks, loaded on demand by
// `loadMessages` in translate.ts (ADR 0014); only the typed reference is here.
