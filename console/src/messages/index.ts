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
import { uiZhCN } from "./ui.zh-CN";
import { libZhCN } from "./lib.zh-CN";
import { formatsZhCN } from "./formats.zh-CN";
import { artifactDetailZhCN } from "./artifact-detail.zh-CN";
import { repositoryZhCN } from "./repository.zh-CN";
import { auditZhCN } from "./audit.zh-CN";
import { dashboardZhCN } from "./dashboard.zh-CN";
import { searchZhCN } from "./search.zh-CN";
import { operationsZhCN } from "./operations.zh-CN";
import { migrationZhCN } from "./migration.zh-CN";
import { nexusBatchZhCN } from "./nexus-batch.zh-CN";
import { nexusScheduleZhCN } from "./nexus-schedule.zh-CN";
import { systemZhCN } from "./system.zh-CN";
import { groupsZhCN } from "./groups.zh-CN";
import { settingsZhCN } from "./settings.zh-CN";
import { repositoryGrantsZhCN } from "./repository-grants.zh-CN";
import { accessZhCN } from "./access.zh-CN";
import { identityZhCN } from "./identity.zh-CN";
import { publicBrowseZhCN } from "./public-browse.zh-CN";
import { shellZhCN } from "./shell.zh-CN";
import { accessFrCA } from "./access.fr-CA";
import { artifactDetailFrCA } from "./artifact-detail.fr-CA";
import { auditFrCA } from "./audit.fr-CA";
import { dashboardFrCA } from "./dashboard.fr-CA";
import { formatsFrCA } from "./formats.fr-CA";
import { groupsFrCA } from "./groups.fr-CA";
import { identityFrCA } from "./identity.fr-CA";
import { libFrCA } from "./lib.fr-CA";
import { migrationFrCA } from "./migration.fr-CA";
import { nexusBatchFrCA } from "./nexus-batch.fr-CA";
import { nexusScheduleFrCA } from "./nexus-schedule.fr-CA";
import { operationsFrCA } from "./operations.fr-CA";
import { publicBrowseFrCA } from "./public-browse.fr-CA";
import { repositoryGrantsFrCA } from "./repository-grants.fr-CA";
import { repositoryFrCA } from "./repository.fr-CA";
import { searchFrCA } from "./search.fr-CA";
import { settingsFrCA } from "./settings.fr-CA";
import { shellFrCA } from "./shell.fr-CA";
import { systemFrCA } from "./system.fr-CA";
import { uiFrCA } from "./ui.fr-CA";

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
};

export type MessageKey = keyof typeof enCA;

const zhCNMessages = {
  ...uiZhCN,
  ...libZhCN,
  ...formatsZhCN,
  ...artifactDetailZhCN,
  ...repositoryZhCN,
  ...auditZhCN,
  ...dashboardZhCN,
  ...searchZhCN,
  ...operationsZhCN,
  ...migrationZhCN,
  ...nexusBatchZhCN,
  ...nexusScheduleZhCN,
  ...systemZhCN,
  ...groupsZhCN,
  ...settingsZhCN,
  ...repositoryGrantsZhCN,
  ...accessZhCN,
  ...identityZhCN,
  ...publicBrowseZhCN,
  ...shellZhCN,
};

/**
 * Excess zh-CN keys fail type checking: a key removed or renamed in en-CA must
 * go from zh-CN in the same change (`scripts/i18n-prune.mjs` does it).
 */
type AssertNoExcessKeys<T extends never> = T;
export type ZhCNExcessKeys = AssertNoExcessKeys<
  Exclude<keyof typeof zhCNMessages, MessageKey>
>;

const frCAMessages = {
  ...accessFrCA,
  ...artifactDetailFrCA,
  ...auditFrCA,
  ...dashboardFrCA,
  ...formatsFrCA,
  ...groupsFrCA,
  ...identityFrCA,
  ...libFrCA,
  ...migrationFrCA,
  ...nexusBatchFrCA,
  ...nexusScheduleFrCA,
  ...operationsFrCA,
  ...publicBrowseFrCA,
  ...repositoryGrantsFrCA,
  ...repositoryFrCA,
  ...searchFrCA,
  ...settingsFrCA,
  ...shellFrCA,
  ...systemFrCA,
  ...uiFrCA,
};

/** The first-class translation: every key, enforced by a test (ADR 0014). */
export const frCA: Partial<Record<MessageKey, string>> = frCAMessages;

type AssertNoExcessFrKeys<T extends never> = T;
export type FrCAExcessKeys = AssertNoExcessFrKeys<
  Exclude<keyof typeof frCAMessages, MessageKey>
>;

/** A lagging, partial overlay: a missing key falls back to en-CA. */
export const zhCN: Partial<Record<MessageKey, string>> = zhCNMessages;
