import type { MessageKey } from ".";
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

// One merged module per locale, loaded on demand by `loadMessages` (ADR 0014).
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

/** A lagging, partial overlay: a missing key falls back to en-CA. */
export const zhCN: Partial<Record<MessageKey, string>> = zhCNMessages;
