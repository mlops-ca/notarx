import type { MessageKey } from ".";
import { accessFrCA } from "./access.fr-CA";
import { assertionsFrCA } from "./assertions.fr-CA";
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
import { workloadFrCA } from "./workload.fr-CA";

// One merged module per locale, loaded on demand by `loadMessages` (ADR 0014).
const frCAMessages = {
  ...accessFrCA,
  ...artifactDetailFrCA,
  ...assertionsFrCA,
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
  ...workloadFrCA,
};

/**
 * The first-class translation: total by type, so a key missing from fr-CA
 * (or left over after en-CA dropped it) fails type checking (ADR 0014).
 */
export const frCA: Record<MessageKey, string> = frCAMessages;
