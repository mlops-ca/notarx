import type { operationsEnCA } from "./operations.en-CA";

export const operationsFrCA: Record<keyof typeof operationsEnCA, string> = {
  "operations.artifactRetention": "Conservation des artefacts",
  "operations.artifactPromotion": "Promotion des artefacts",
  "operations.repositoryReplication": "Réplication des dépôts",
  "operations.objectReclamation": "Récupération des objets",
  "operations.intelligenceSync": "Synchronisation du renseignement",
  "operations.auditRetention": "Conservation de l’audit",
  "operations.historicalJob": "Tâche historique",
  "operations.format": "Format",
  "operations.sourceRepositoryID": "ID du dépôt source",
  "operations.coordinate": "Coordonnée",
  "operations.global": "Global",
  "operations.attemptsNotReported": "Tentatives non signalées",
  "operations.attempts": "{attempts} / {maxAttempts} tentatives",
  "operations.nextRun": "Prochaine exécution",
  "operations.completed": "Terminée",
  "operations.created": "Créée",
  "operations.started": "Démarrée",
  "operations.hideJobDetails": "Masquer les détails de la tâche",
  "operations.viewJobDetails": "Afficher les détails de la tâche",
  "operations.queueForImmediateExecution":
    "Mettre en file pour exécution immédiate",
  "operations.retryJob": "Relancer la tâche",
  "operations.cancelThisJob": "Annuler cette tâche?",
  "operations.youCanRetryItLaterFromOperations":
    "Vous pourrez la relancer plus tard depuis Opérations.",
  "operations.cancelJob": "Annuler la tâche",
  "operations.back": "Retour",
  "operations.jobID": "ID de la tâche",
  "operations.repository": "Dépôt",
  "operations.globalJob": "Tâche globale",
  "operations.thisJobDidNotReportAdditionalExecutionDetails":
    "Cette tâche n’a signalé aucun détail d’exécution supplémentaire.",
  "operations.job": "Tâche",
  "operations.statusAndProgress": "État et progression",
  "operations.timeline": "Chronologie",
  "operations.actions": "Actions",
  "operations.jobs": "Tâches",
  "operations.currentRetentionWindow": "Fenêtre de conservation actuelle",
  "operations.running": "En cours d’exécution",
  "operations.inProgress": "En cours",
  "operations.pending": "En attente",
  "operations.waitingForAWorker": "En attente d’un worker",
  "operations.failed": "Échouées",
  "operations.reviewFailureDetails": "Examiner les détails de l’échec",
  "operations.noFailedJobs": "Aucune tâche échouée",
  "operations.jobHistory": "Historique des tâches",
  "operations.showingOfJobs": "Affichage de {length} tâches sur {length2}",
  "operations.loadingJobRecords": "Chargement des enregistrements de tâches",
  "operations.refresh": "Actualiser",
  "operations.clearFilters": "Effacer les filtres",
  "operations.status": "État",
  "operations.allStatuses": "Tous les états",
  "operations.jobType": "Type de tâche",
  "operations.allTypes": "Tous les types",
  "operations.allRepositories": "Tous les dépôts",
  "operations.loadingJobStatus": "Chargement de l’état des tâches…",
  "operations.noMatchingJobs": "Aucune tâche correspondante",
  "operations.pageScopeNotice":
    "Le sommaire et les filtres ne couvrent que les {length} tâches chargées sur cette page. Les tâches plus anciennes se trouvent sur les pages suivantes; utilisez la pagination pour les charger.",
  "operations.adjustTheFiltersOrRefreshAfterNewJobs":
    "Ajustez les filtres ou actualisez après la création de nouvelles tâches.",
  "operations.everyMinutes": "Toutes les {minutes} minutes",
  "operations.editScheduledTask": "Modifier la tâche planifiée",
  "operations.newScheduledTask": "Nouvelle tâche planifiée",
  "operations.cancel": "Annuler",
  "operations.save": "Enregistrer",
  "operations.create": "Créer",
  "operations.taskName": "Nom de la tâche",
  "operations.eGDailySnapshotCleanup":
    "p. ex. Nettoyage quotidien des snapshots",
  "operations.taskAction": "Action de la tâche",
  "operations.runRepositoryRetention": "Exécuter la conservation du dépôt",
  "operations.runAuditRetention": "Exécuter la conservation de l’audit",
  "operations.targetRepository": "Dépôt cible",
  "operations.onlyActiveHostedRepositoriesWithRetentionSupportAre":
    "Seuls les dépôts hébergés actifs qui prennent en charge la conservation sont listés.",
  "operations.selectARepository": "Sélectionner un dépôt",
  "operations.runInterval": "Intervalle d’exécution",
  "operations.minimum15MinutesOnlyOneDispatchIsRecovered":
    "Minimum de 15 minutes; une seule répartition est rattrapée après une interruption.",
  "operations.minutes": "minutes",
  "operations.enabled": "Activée",
  "operations.disabled": "Désactivée",
  "operations.description": "Description",
  "operations.trigger": "Déclencheur",
  "operations.manual": "Manuel",
  "operations.scheduled": "Planifié",
  "operations.dispatchStatus": "État de la répartition",
  "operations.triggered": "Déclenchée",
  "operations.downstreamJob": "Tâche en aval",
  "operations.failureReason": "Motif de l’échec",
  "operations.dispatchHistory": "Historique des répartitions",
  "operations.noDispatchHistory": "Aucun historique de répartition",
  "operations.eachDispatchIsRecordedHereOnceTheSchedule":
    "Chaque répartition est consignée ici une fois la planification déclenchée.",
  "operations.task": "Tâche",
  "operations.noDescription": "Aucune description",
  "operations.action": "Action",
  "operations.repositoryRetention": "Conservation du dépôt",
  "operations.target": "Cible",
  "operations.globalAuditLog": "Journal d’audit global",
  "operations.lastDispatch": "Dernière répartition",
  "operations.enabled2": "Activée",
  "operations.enableScheduledTask": "Activer la tâche planifiée",
  "operations.runNow": "Exécuter maintenant",
  "operations.edit": "Modifier",
  "operations.dispatchHistoryIsRemovedAlreadySubmittedJobsAre":
    "L’historique des répartitions est supprimé; les tâches déjà soumises ne sont pas touchées.",
  "operations.delete": "Supprimer",
  "operations.scheduledTasks": "Tâches planifiées",
  "operations.newSchedule": "Nouvelle planification",
  "operations.loadingSchedules": "Chargement des planifications…",
  "operations.noScheduledTasksYet": "Aucune tâche planifiée",
  "operations.createAScheduleToDispatchExistingRepositoryOr":
    "Créez une planification pour répartir à intervalle fixe les politiques de conservation de dépôt ou d’audit existantes.",
  "operations.editWebhookSubscription": "Modifier l’abonnement webhook",
  "operations.newWebhookSubscription": "Nouvel abonnement webhook",
  "operations.subscriptionName": "Nom de l’abonnement",
  "operations.eGSecurityAutomation": "p. ex. Automatisation de la sécurité",
  "operations.hTTPSEndpoint": "Point de terminaison HTTPS",
  "operations.productionDeliveryRejectsPrivateLoopbackAndRedirectTargets":
    "En production, la livraison rejette les cibles privées, de bouclage et de redirection.",
  "operations.events": "Événements",
  "operations.supportsArtifactQuarantineAndReleasePlusFinalCargo":
    "Les types d’événements sont livrés dès que la fonctionnalité qui les émet est disponible; les événements de mise en quarantaine, de libération, d’analyse ainsi que de promotion et de réplication Cargo sont émis aujourd’hui.",
  "operations.hMACSigningSecret": "Secret de signature HMAC",
  "operations.leaveBlankToKeepTheCurrentSecretEntering":
    "Laissez vide pour conserver le secret actuel; en saisir un le renouvelle immédiatement.",
  "operations.atLeast32CharactersItIsNeverShown":
    "Au moins 32 caractères; il ne sera plus jamais affiché après l’enregistrement.",
  "operations.atLeast32Characters": "Au moins 32 caractères",
  "operations.receiveNewEvents": "Recevoir les nouveaux événements",
  "operations.pauseNewDeliveries": "Suspendre les nouvelles livraisons",
  "operations.subscription": "Abonnement",
  "operations.event": "Événement",
  "operations.state": "État",
  "operations.attempts2": "Tentatives",
  "operations.lastResult": "Dernier résultat",
  "operations.updated": "Mis à jour",
  "operations.replayDelivery": "Rejouer la livraison",
  "operations.recentDeliveries": "Livraisons récentes",
  "operations.deliveryState": "État de la livraison",
  "operations.allStates": "Tous les états",
  "operations.loadingDeliveries": "Chargement des livraisons…",
  "operations.noDeliveriesYet": "Aucune livraison",
  "operations.matchingArtifactQuarantineEventsAppearHereAfterDelivery":
    "Les événements de quarantaine d’artefacts correspondants apparaissent ici une fois la livraison planifiée.",
  "operations.endpoint": "Point de terminaison",
  "operations.events2": "Événements",
  "operations.signing": "Signature",
  "operations.configured": "Configurée",
  "operations.missing": "Manquante",
  "operations.enableWebhookSubscription": "Activer l’abonnement webhook",
  "operations.editSubscription": "Modifier l’abonnement",
  "operations.webhookSubscriptions": "Abonnements webhook",
  "operations.newSubscription": "Nouvel abonnement",
  "operations.loadingWebhooks": "Chargement des webhooks…",
  "operations.noWebhookSubscriptionsYet": "Aucun abonnement webhook",
  "operations.createASubscriptionToDeliverQuarantineStateChanges":
    "Créez un abonnement pour livrer les changements d’état de quarantaine aux systèmes d’automatisation.",
  "operations.operations": "Opérations",
  "operations.scheduleRecurringMaintenanceAndTrackBackgroundJobsAcross":
    "Planifiez la maintenance récurrente et suivez les tâches en arrière-plan dans tous les dépôts.",
  "operations.webhookDelivery": "Livraison de webhook",
  "operations.logFilters": "Filtres de journaux",
  "operations.keyword": "Mot-clé",
  "operations.searchLogContent": "Rechercher dans le contenu des journaux",
  "operations.timeRange": "Plage de temps",
  "operations.last15Minutes": "15 dernières minutes",
  "operations.lastHour": "Dernière heure",
  "operations.last24Hours": "24 dernières heures",
  "operations.custom": "Personnalisée",
  "operations.level": "Niveau",
  "operations.allLevels": "Tous les niveaux",
  "operations.startAndEndLocalTime": "Début et fin (heure locale)",
  "operations.startDate": "Date de début",
  "operations.endDate": "Date de fin",
  "operations.atMost24HoursEndNoLaterThan":
    "Au plus 24 heures; la fin ne doit pas dépasser 5 minutes à partir de maintenant.",
  "operations.component": "Composant",
  "operations.allComponentsEnterExactName":
    "Tous les composants; saisir le nom exact",
  "operations.findARequest": "Trouver une requête",
  "operations.moreFilters": "Plus de filtres",
  "operations.instance": "Instance",
  "operations.currentProcess": "Processus actuel",
  "operations.correlateATrace": "Corréler une trace",
  "operations.adjustTheTimeRange": "Ajuster la plage de temps",
  "operations.fixedSnapshotChooseARecentRangeToFollow":
    "Snapshot figé; choisissez une plage récente pour suivre les nouveaux journaux.",
  "operations.recentRangesUpdateOnEveryQueryAndFollow":
    "Les plages récentes se mettent à jour à chaque requête et lecture de suivi.",
  "operations.refreshSnapshot": "Actualiser le snapshot",
  "operations.search": "Rechercher",
  "operations.copied": "Copié",
  "operations.copy": "Copier",
  "operations.invalidTimeRangeAtMost24HoursEnding":
    "Plage de temps invalide\u00a0: au plus 24 heures, se terminant au plus tard 5 minutes à partir de maintenant. Ajustez la plage et relancez la recherche.",
  "operations.memoryLogQueriesAreDisabledOnThisNode":
    "Les requêtes sur les journaux en mémoire sont désactivées sur ce nœud. Vérifiez la configuration du tampon.",
  "operations.logSourceIdentityIsUnavailableCheckTheNode":
    "L’identité de la source des journaux est indisponible. Vérifiez la configuration du nœud et réessayez.",
  "operations.thisEndpointQueriesTheConnectedProcessOnlyClear":
    "Ce point de terminaison n’interroge que le processus connecté. Effacez le filtre d’instance et réessayez.",
  "operations.processOrSessionChangedRefreshTheSnapshot":
    "Le processus ou la session a changé; actualisez le snapshot.",
  "operations.theCursorIsInvalidOrFiltersChangedRefresh":
    "Le curseur est invalide ou les filtres ont changé. Actualisez le snapshot.",
  "operations.logQueryTimedOutRetryShortly":
    "La requête sur les journaux a expiré. Réessayez sous peu.",
  "operations.thisIdentityCannotQueryRuntimeLogs":
    "Cette identité ne peut pas interroger les journaux d’exécution.",
  "operations.updateYourPasswordBeforeQueryingRuntimeLogs":
    "Mettez à jour votre mot de passe avant d’interroger les journaux d’exécution.",
  "operations.aFilterIsTooLongOrContainsControl":
    "Un filtre est trop long ou contient des caractères de contrôle. Ajustez-le et relancez la recherche.",
  "operations.logQueryFailedCheckTheConnectionOrPermissions":
    "La requête sur les journaux a échoué. Vérifiez la connexion ou les permissions et réessayez.",
  "operations.logQueryFailedCheckTheConnectionAndRetry":
    "La requête sur les journaux a échoué. Vérifiez la connexion et réessayez.",
  "operations.runtimeLogs": "Journaux d’exécution",
  "operations.searchThisProcessSInMemoryLogsRestart":
    "Recherchez dans les journaux en mémoire de ce processus; un redémarrage les efface.",
  "operations.processOrSessionChangedRefreshTheSnapshot2":
    "Le processus ou la session a changé; actualisez le snapshot",
  "operations.queryingRuntimeLogs": "Interrogation des journaux d’exécution…",
  "operations.session": "Session",
  "operations.loadedBytes": "{length} chargés · {bytes} octets",
  "operations.retainedSequences": "Séquences conservées",
  "operations.notReported": "Non signalé",
  "operations.minimumRequestsSlowMsBufferLines":
    "Minimum {minimumLevel} · requêtes {accessMode} · lentes {slowThresholdMs}ms · tampon {capacityLines} lignes",
  "operations.gatewayDidNotReportItsLoggingPolicy":
    "La passerelle n’a pas signalé sa politique de journalisation",
  "operations.pauseFollow": "Suspendre le suivi",
  "operations.followNewLogs": "Suivre les nouveaux journaux",
  "operations.pollingPausedWhileHidden":
    "Interrogation suspendue pendant que la page est masquée",
  "operations.unreadResumeAtBottom": "{unread} non lus · Reprendre en bas",
  "operations.wrapLines": "Retour à la ligne",
  "operations.copySelection": "Copier la sélection",
  "operations.copyLoaded": "Copier les journaux chargés",
  "operations.downloadNDJSON": "Télécharger en NDJSON",
  "operations.queryAndCopyDetails": "Détails de la requête et de la copie",
  "operations.pausePreservesTheCursorForResumeReadingAbove":
    "La suspension conserve le curseur pour la reprise; lire au-dessus du bas ne force pas le défilement.",
  "operations.copyAndExportLoadedResultsOnlyAtMost":
    "La copie et l’exportation ne portent que sur les résultats chargés, au plus {MAX_LOG_RECORDS} lignes / 1 MiB; les champs affichés sont limités à {MAX_DISPLAY_CHARS} caractères. Une sélection de plus de 1 MiB est rejetée. Un filtre DEBUG n’active pas DEBUG; des résultats vides n’indiquent pas pourquoi des événements sont absents.",
  "operations.selectionExceeds1MiBSelectLessTextTo":
    "La sélection dépasse 1 MiB; sélectionnez moins de texte pour copier",
  "operations.theComponentListIsBoundedEnterAnotherExact":
    "La liste des composants est limitée; saisissez un autre nom exact. worker n’est pas un caractère générique.",
  "operations.currentCorrelationFilters": "Filtres de corrélation actuels",
  "operations.serverRetentionOverwroteSomeUnreadLogs":
    "La conservation du serveur a écrasé certains journaux non lus",
  "operations.changedToANewProcessOrSessionPrevious":
    "Passage à un nouveau processus ou à une nouvelle session; les enregistrements précédents sont effacés",
  "operations.clientCapacityRemovedRowsThisDoesNotIndicate":
    "La capacité du client a retiré {trimmed} lignes; cela n’indique pas une perte de journaux côté serveur",
  "operations.noLogsInThisRange": "Aucun journal dans cette plage",
  "operations.tryTheLast24HoursOrDifferentFilters":
    "Essayez les 24 dernières heures ou d’autres filtres. Les journaux ne sont conservés que dans ce processus.",
  "operations.runtimeLogStream": "Flux des journaux d’exécution",
  "operations.loadOlderLogs": "Charger les journaux plus anciens",
  "operations.instance2": "Instance",
  "operations.connectedNode": "Nœud connecté",
  "operations.buildVersion": "Version de build",
  "operations.developmentBuild": "Build de développement",
  "operations.versionUnknown": "Version inconnue",
  "operations.revisionUnknown": "Révision inconnue",
  "operations.roles": "Rôles",
  "operations.workerCapabilities": "Capacités du worker",
  "operations.noFormatWorker": "Aucun worker de format",
  "operations.started2": "Démarré",
  "operations.lastHeartbeat": "Dernier heartbeat",
  "operations.stopped": "Arrêté",
  "operations.runtimeNodes": "Nœuds d’exécution",
  "operations.instances": "{length} instances",
  "operations.loading": "Chargement",
  "operations.refreshRuntimeNodes": "Actualiser les nœuds d’exécution",
  "operations.clusterCapabilitiesNeedAttention":
    "Les capacités de la grappe demandent attention",
  "operations.sessions": "Sessions",
  "operations.noReleaseSourceIsConfiguredSoTheLatest":
    "Aucune source de versions n’est configurée; la dernière version ne peut donc pas être déterminée.",
  "operations.loadingRuntimeNodes": "Chargement des nœuds d’exécution…",
  "operations.noNodeHeartbeatsReceived": "Aucun heartbeat de nœud reçu",
  "operations.nodesAppearAutomaticallyAfterStartup":
    "Les nœuds apparaissent automatiquement après le démarrage.",
  "operations.temporary": "Temporaire",
  "operations.logs": "Journaux",
  "operations.backups": "Sauvegardes",
  "operations.noObservationDirectoryConfiguredCapacityUnknown":
    "Aucun répertoire d’observation configuré; capacité inconnue",
  "operations.remoteFilesystemCapacityUnknown":
    "Système de fichiers distant; capacité inconnue",
  "operations.unsupportedFilesystemCapacityUnknown":
    "Système de fichiers non pris en charge; capacité inconnue",
  "operations.unsupportedPlatformCapacityUnknown":
    "Plateforme non prise en charge; capacité inconnue",
  "operations.directoryMetadataUnavailableCapacityUnknown":
    "Métadonnées du répertoire indisponibles; capacité inconnue",
  "operations.filesystemIdentityUnavailableCapacityUnknown":
    "Identité du système de fichiers indisponible; capacité inconnue",
  "operations.invalidSampleCapacityUnknown":
    "Échantillon invalide; capacité inconnue",
  "operations.byteCountExceedsSupportedRangeCapacityUnknown":
    "Le nombre d’octets dépasse la plage prise en charge; capacité inconnue",
  "operations.refreshTimedOutCapacityUnknown":
    "L’actualisation a expiré; capacité inconnue",
  "operations.queryCancelledCapacityUnknown":
    "Requête annulée; capacité inconnue",
  "operations.localMountCapacity": "Capacité du point de montage local",
  "operations.thisProcessSMountViewIsNotThe":
    "La vue des montages de ce processus n’est ni le pool physique du backend S3 ou NAS ni un quota logique de dépôt. N’additionnez pas la capacité des alias partagés.",
  "operations.thisNodeDoesNotReportLocalCapacityCapacity":
    "Ce nœud ne signale pas de capacité locale; capacité inconnue",
  "operations.snapshotChecked": "Snapshot vérifié",
  "operations.refreshInterval": "Intervalle d’actualisation",
  "operations.maximumSampleAge": "Âge maximal de l’échantillon",
  "operations.validSample": "Échantillon valide",
  "operations.stale": "Périmé",
  "operations.unknown": "Inconnu",
  "operations.sampleUnavailableOrInvalidCapacityUnknown":
    "Échantillon indisponible ou invalide; capacité inconnue",
  "operations.total": "Total",
  "operations.available": "Disponible",
  "operations.previousSample": "Échantillon précédent",
  "operations.sampleTime": "Heure de l’échantillon",
  "operations.trustedObservationUnavailable": "Observation fiable indisponible",
  "operations.windowWarmingUpInsufficientData":
    "Fenêtre en préchauffage; données insuffisantes",
  "operations.processSessionChangedWindowWarmingUpAgain":
    "La session du processus a changé; la fenêtre se préchauffe de nouveau",
  "operations.counterResetWindowWarmingUpAgain":
    "Compteur réinitialisé; la fenêtre se préchauffe de nouveau",
  "operations.samplingGapWindowWarmingUpAgain":
    "Interruption de l’échantillonnage; la fenêtre se préchauffe de nouveau",
  "operations.invalidSampleClockRatioUnknown":
    "Horloge d’échantillon invalide; ratio inconnu",
  "operations.countsExceedTheSafeRangeRatioUnknown":
    "Les décomptes dépassent la plage sûre; ratio inconnu",
  "operations.noRequestsInTheWindowRatioUnknown":
    "Aucune requête dans la fenêtre; ratio inconnu",
  "operations.fewerThan20RequestsInsufficientData":
    "Moins de 20 requêtes; données insuffisantes",
  "operations.sampleIsStaleRatioUnknown": "Échantillon périmé; ratio inconnu",
  "operations.businessHTTP5xxObservation": "Observation des HTTP 5xx métier",
  "operations.thisProcessSBusinessHTTP1xx5xxRequests":
    "HTTP métier de ce processus\u00a0: requêtes 1xx à 5xx, les 5xx étant comptées comme des erreurs. Comprend l’API de gestion; exclut les sondes de santé et de métriques. Le ratio n’est ni un jugement de santé ni une alerte.",
  "operations.sampleAvailable": "Échantillon disponible",
  "operations.insufficientDataOrUnknown": "Données insuffisantes ou inconnues",
  "operations.sampleUnavailableOrInvalidRatioUnknown":
    "Échantillon indisponible ou invalide; ratio inconnu",
  "operations.5xxRatio": "Ratio de 5xx",
  "operations.previousSnapshotRequests": "Requêtes du snapshot précédent",
  "operations.requests": "Requêtes",
  "operations.previousSnapshot5xx": "5xx du snapshot précédent",
  "operations.5xxErrors": "Erreurs 5xx",
  "operations.observedNodeSession": "Nœud / session observés",
  "operations.actualWindow": "Fenêtre réelle",
  "operations.sampleSnapshotChecked": "Échantillon / snapshot vérifié",
  "operations.targetWindow300sMinimum20RequestsSampleEvery":
    "Fenêtre cible de 300 s · minimum de 20 requêtes · échantillon toutes les 15 s · âge maximal de l’échantillon de 30 s. Les décomptes de préchauffage ne couvrent que la fenêtre réelle; 20 requêtes ne garantissent pas une confiance statistique.",
  "operations.healthy": "En santé",
  "operations.degraded": "Dégradé",
  "operations.unhealthy": "Défaillant",
  "operations.unreachable": "Inaccessible",
  "operations.notConfigured": "Non configuré",
  "operations.externalScannerIsNotConfigured":
    "L’analyseur externe n’est pas configuré",
  "operations.healthReportingUnavailableDatabaseFreshnessUnknown":
    "Rapport de santé indisponible · Fraîcheur de la base de données inconnue",
  "operations.vulnerabilityDatabaseUpdateTimeUnavailable":
    "Heure de mise à jour de la base de données des vulnérabilités indisponible",
  "operations.healthCheckFailed":
    "Échec de la vérification de santé · {databaseDetail}",
  "operations.scannerReportsUnhealthy":
    "L’analyseur signale un état défaillant · {databaseDetail}",
  "operations.scannerReportsDegraded":
    "L’analyseur signale un état dégradé · {databaseDetail}",
  "operations.needsAttention": "Demande attention",
  "operations.items": "{attentionCount} éléments",
  "operations.dependencyUnavailable": "Dépendance indisponible",
  "operations.scannerTrust": "Fiabilité de l’analyseur",
  "operations.runtimeNodeHealthDegraded":
    "Santé des nœuds d’exécution dégradée",
  "operations.staleAndOffline": "{stale} périmés et {offline} hors ligne",
  "operations.failedQueue": "File des échecs",
  "operations.jobs2": "{count} tâches",
  "operations.buildAndRuntimeIdentity": "Identité de build et d’exécution",
  "operations.buildInformation": "Informations de build",
  "operations.consoleVersion": "Version de la Console",
  "operations.gatewayVersion": "Version de la passerelle",
  "operations.gatewayRevision": "Révision de la passerelle",
  "operations.workspace": "Espace de travail",
  "operations.modified": "Modifié",
  "operations.clean": "Propre",
  "operations.consoleAndGatewayVersionsDiffer":
    "Les versions de la Console et de la passerelle diffèrent",
  "operations.imageTagsMayDifferOrTheBrowserMay":
    "Les tags d’image peuvent différer, ou le navigateur a peut-être mis en cache un ancien bundle. Vérifiez les versions déployées et actualisez la page.",
  "operations.runtimeIdentity": "Identité d’exécution",
  "operations.workerFormats": "Formats des workers",
  "operations.workerJobs": "Tâches des workers",
  "operations.objectStorage": "Stockage d’objets",
  "operations.runtimeDependencies": "Dépendances d’exécution",
  "operations.dependenciesAndScanner": "Dépendances et analyseur",
  "operations.reachable": "Accessible",
  "operations.unreachable2": "Inaccessible",
  "operations.noDependencyChecksReported":
    "Aucune vérification de dépendance signalée",
  "operations.artifactScanner": "Analyseur d’artefacts",
  "operations.formats": "Formats",
  "operations.count": "Nombre",
  "operations.oldestQueued": "Plus ancienne en file",
  "operations.backgroundQueues": "Files d’arrière-plan",
  "operations.noActiveQueueEntries": "Aucune entrée de file active",
  "operations.backgroundJobsAppearHereAsSoonAsThey":
    "Les tâches en arrière-plan apparaissent ici dès qu’elles occupent une file.",
  "operations.loadingDiagnostics": "Chargement des diagnostics…",
  "operations.refreshFailedShowingThePreviousDiagnosticSnapshot":
    "Échec de l’actualisation; affichage du snapshot de diagnostic précédent",
  "operations.diagnosticSnapshot": "Snapshot de diagnostic",
  "operations.generated": "Généré",
  "operations.copySanitizedDiagnosticsJSON":
    "Copier le JSON de diagnostic épuré",
  "operations.refreshDiagnostics": "Actualiser les diagnostics",
  "operations.systemHealthNeedsAttention":
    "La santé du système demande attention",
  "operations.resolveTheDependencyScannerNodeCapabilityOrFailed":
    "Résolvez les problèmes de dépendance, d’analyseur, de capacité de nœud ou de file des échecs ci-dessous avant la maintenance.",
  "operations.dependenciesReady": "Dépendances prêtes",
  "operations.checks": "{length} vérifications",
  "operations.onlineNodes": "Nœuds en ligne",
  "operations.staleOffline": "{stale} périmés · {offline} hors ligne",
  "operations.queuedWork": "Travaux en file",
  "operations.pendingRunningRetryingAndFailed":
    "En attente, en cours, en nouvelle tentative et échoués",
  "operations.failedWork": "Travaux échoués",
  "operations.needsReview": "À examiner",
  "operations.noFailures": "Aucun échec",
  "operations.postgresql": "PostgreSQL",
  "operations.vulnerabilityDatabaseFresh":
    "Base de données des vulnérabilités à jour · {date}",
  "operations.vulnerabilityDatabaseStale":
    "Base de données des vulnérabilités périmée · {date}",
  "operations.sharesTheVisibleFilesystemWith":
    "Partage le système de fichiers visible avec {aliases}",
  "operations.chooseBothStartAndEndTimes":
    "Choisissez les heures de début et de fin.",
  "operations.startMustNotBeAfterEnd":
    "Le début ne doit pas être postérieur à la fin.",
  "operations.timeRangeCannotExceed24Hours":
    "La plage de temps ne peut pas dépasser 24 heures.",
  "operations.everyDay": "Tous les {days} jour",
  "operations.everyDays": "Tous les {days} jours",
  "operations.everyHour": "Toutes les {hours} heure",
  "operations.everyHours": "Toutes les {hours} heures",
  "operations.endCannotBeMoreThan5MinutesFromNow":
    "La fin ne peut pas dépasser 5 minutes à partir de maintenant.",
  "operations.schedule": "Planification",
  "operations.moreActionsForTask": "Plus d’actions pour {name}",
  "operations.deleteScheduledTask": "Supprimer la planification",
  "operations.deleteScheduledTaskNamed":
    "Supprimer la tâche planifiée «\u00a0{name}\u00a0»?",
  "operations.paused": "Suspendue",
  "operations.noOnlineApiNodes": "Aucun nœud d’API en ligne",
  "operations.noOnlineSchedulerNodes":
    "Aucun nœud planificateur en ligne; la planification en arrière-plan ne s’exécutera pas",
  "operations.noOnlineWorkerNodes":
    "Aucun nœud worker en ligne; les tâches en arrière-plan ne seront pas traitées",
  "operations.duplicateInstanceIds":
    "Des ID d’instance en double existent; leurs sessions sont enregistrées séparément",
  "operations.staleHeartbeatNodes":
    "Des nœuds d’exécution ont des heartbeats périmés",
  "operations.buildIdentityUnknown":
    "Certains nœuds en ligne n’ont pas signalé une identité de build complète; la cohérence des versions ne peut pas être confirmée",
  "operations.mixedBuild":
    "Les nœuds en ligne exécutent des versions ou des révisions différentes; une mise à niveau progressive est peut-être en cours",
};
