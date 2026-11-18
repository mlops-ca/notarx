import type { systemEnCA } from "./system.en-CA";

export const systemFrCA: Record<keyof typeof systemEnCA, string> = {
  "system.notFiring": "Non déclenchée",
  "system.warning": "Avertissement",
  "system.critical": "Critique",
  "system.recovered": "Rétablie",
  "system.sampleAvailable": "Échantillon disponible",
  "system.capacityUnknown": "Capacité inconnue",
  "system.sampleStale": "Échantillon périmé",
  "system.repositoryQuotaNotConfigured": "Quota du dépôt non configuré",
  "system.configurationChanged": "Configuration modifiée",
  "system.disabled": "Désactivée",
  "system.deletedHistoryRetained": "Supprimée; historique conservé",
  "system.repositoryDeleted": "Dépôt supprimé",
  "system.repositoryInactive": "Dépôt inactif",
  "system.pendingDelivery": "Livraison en attente",
  "system.delivering": "Livraison en cours",
  "system.retryScheduled": "Nouvelle tentative planifiée",
  "system.acceptedBySMTPServer": "Accepté par le serveur SMTP",
  "system.deliveryStopped": "Livraison arrêtée",
  "system.queuedForDelivery": "Mis en file pour livraison",
  "system.deliveryQueueFull": "File de livraison pleine",
  "system.emailChannelDisabled": "Canal de courriel désactivé",
  "system.encryptionKeyUnavailable": "Clé de chiffrement indisponible",
  "system.emailTargetDisabled": "Cible de courriel désactivée",
  "system.emailTargetVersionChanged":
    "Version de la cible de courriel modifiée",
  "system.emailTargetUnavailable": "Cible de courriel indisponible",
  "system.automaticDeliveryCancelledAfterRuleChange":
    "Livraison automatique annulée après modification de la règle",
  "system.automaticDeliveryCancelledAfterDisablingTheRule":
    "Livraison automatique annulée après désactivation de la règle",
  "system.automaticDeliveryCancelledAfterDeletingTheRule":
    "Livraison automatique annulée après suppression de la règle",
  "system.networkConnectionFailed": "Échec de la connexion réseau",
  "system.tLSVerificationFailed": "Échec de la vérification TLS",
  "system.sMTPServerRejectedTheMessage": "Le serveur SMTP a rejeté le message",
  "system.temporarySMTPFailure": "Échec SMTP temporaire",
  "system.deliveryLeaseExpired": "Bail de livraison expiré",
  "system.retryAttemptsExhausted": "Tentatives épuisées",
  "system.messageValidationFailed": "Échec de la validation du message",
  "system.emailRelayResolutionFailed":
    "Échec de la résolution du relais de courriel",
  "system.emailRelayAddressNotApproved":
    "Adresse du relais de courriel non approuvée",
  "system.emailRelayConnectionFailed":
    "Échec de la connexion au relais de courriel",
  "system.tLSConfigurationUnavailable": "Configuration TLS indisponible",
  "system.requiredTLSUnavailable": "TLS requis indisponible",
  "system.emailAuthenticationConfigurationUnavailable":
    "Configuration d’authentification du courriel indisponible",
  "system.emailAuthenticationFailed": "Échec de l’authentification du courriel",
  "system.permanentSMTPRejection": "Rejet SMTP permanent",
  "system.temporarySMTPRejection": "Rejet SMTP temporaire",
  "system.sMTPTransportFailed": "Échec du transport SMTP",
  "system.deliveryOutcomeUnknown": "Résultat de la livraison inconnu",
  "system.statusUnknown": "Statut inconnu",
  "system.anotherAdministratorChangedThisRuleGoBackRefresh":
    "Un autre administrateur a modifié cette règle. Revenez en arrière, actualisez et modifiez de nouveau; la configuration plus récente n’a pas été écrasée.",
  "system.changeYourPasswordBeforeReturningToThisPage":
    "Changez votre mot de passe avant de revenir à cette page.",
  "system.permissionDeniedConfirmPlatformAdministratorAccessAndRefresh":
    "Permission refusée. Confirmez l’accès d’administrateur de la plateforme et actualisez.",
  "system.thisSessionCannotPerformThisActionSignIn":
    "Cette session ne peut pas effectuer cette action. Reconnectez-vous ou confirmez l’accès d’administrateur de la plateforme.",
  "system.theSessionHasExpiredSignInAgain":
    "La session a expiré. Reconnectez-vous.",
  "system.thisRepositoryAlreadyHasARuleRefreshAnd":
    "Ce dépôt a déjà une règle. Actualisez et modifiez la règle existante.",
  "system.theRuleLimitHasBeenReachedIncludingRetained":
    "La limite de règles est atteinte, y compris les enregistrements supprimés conservés.",
  "system.thisRuleWasDeletedGoBackAndRefresh":
    "Cette règle a été supprimée. Revenez en arrière et actualisez.",
  "system.thisRepositoryIsUnavailableForAlertingCheckIts":
    "Ce dépôt est indisponible pour les alertes. Vérifiez son état.",
  "system.configurationValidationFailedCheckThresholdsHoldDurationsAnd":
    "Échec de la validation de la configuration. Vérifiez les seuils, les durées de maintien et la cible, puis réessayez.",
  "system.theResourceOrManagementEndpointIsUnavailableRefresh":
    "La ressource ou le point de terminaison de gestion est indisponible. Actualisez et vérifiez la version de Gateway.",
  "system.theEmailTargetWasDisabledRefreshAndChoose":
    "La cible de courriel a été désactivée. Actualisez et choisissez une cible disponible.",
  "system.theEmailChannelIsDisabledTheRuleCannot":
    "Le canal de courriel est désactivé; la règle ne peut pas être activée.",
  "system.encryptionKeyUnavailableTheRuleCannotBeEnabled":
    "Clé de chiffrement indisponible; la règle ne peut pas être activée.",
  "system.requestFailedCheckTheConnectionAndRefreshBefore":
    "Échec de la requête. Vérifiez la connexion et actualisez avant de réessayer.",
  "system.unknown": "Inconnu",
  "system.enterAPercentageAbove0AndAtMost":
    "Saisissez un pourcentage supérieur à 0 et d’au plus 100, avec au maximum deux décimales.",
  "system.enterIntegerSecondsFrom186400":
    "Saisissez un nombre entier de secondes de 1 à 86400.",
  "system.enterIntegerSecondsFrom303600":
    "Saisissez un nombre entier de secondes de 30 à 3600.",
  "system.recoveryMustBeBelowTheWarningThreshold":
    "Le rétablissement doit être inférieur au seuil d’avertissement.",
  "system.warningMustBeBelowTheCriticalThreshold":
    "L’avertissement doit être inférieur au seuil critique.",
  "system.chooseARepository": "Choisissez un dépôt.",
  "system.chooseAnEmailTarget": "Choisissez une cible de courriel.",
  "system.editRule": "Modifier la règle",
  "system.createDisabledRule": "Créer une règle désactivée",
  "system.loadMoreRepositories": "Charger plus de dépôts",
  "system.repository": "Dépôt",
  "system.repositoryScopeCannotBeChangedAfterCreation":
    "La portée du dépôt ne peut pas être modifiée après la création.",
  "system.chooseARepository2": "Choisissez un dépôt",
  "system.emailTarget": "Cible de courriel",
  "system.onlyTheNameAndConfigurationStatusAreShown":
    "Seuls le nom et l’état de configuration sont affichés; les adresses des destinataires ne sont pas lues.",
  "system.chooseAnEmailTarget2": "Choisissez une cible de courriel",
  "system.ready": "Prête",
  "system.notReady": "Non prête",
  "system.triggerWarning": "Déclencher l’avertissement",
  "system.triggersAfterUsageReachesTheThresholdForThe":
    "Se déclenche lorsque l’utilisation atteint le seuil pendant la durée indiquée.",
  "system.escalateToCritical": "Passer à critique",
  "system.recovery": "Rétablissement",
  "system.recoveryIsNotifiedAfterUsageStaysStrictlyBelow":
    "Le rétablissement est notifié lorsque l’utilisation reste strictement sous ce seuil pendant la durée indiquée.",
  "system.maximumSampleAgeSeconds": "Âge maximal de l’échantillon (secondes)",
  "system.staleOrUnknownEvidenceInterruptsHoldTimingAnd":
    "Des données périmées ou inconnues interrompent le calcul de la durée de maintien et conservent la gravité existante.",
  "system.savingBindsTheCurrentTargetVersionResetsHold":
    "L’enregistrement lie la version actuelle de la cible, réinitialise le calcul de la durée de maintien et conserve la gravité. Les anciennes livraisons automatiques non prises en charge sont annulées; une livraison déjà prise en charge peut se terminer une fois.",
  "system.newRulesAreDisabledByDefaultAndSend":
    "Les nouvelles règles sont désactivées par défaut et n’envoient aucun courriel de test. Vérifiez les prérequis et activez-les explicitement après l’enregistrement.",
  "system.thisRuleIsEnabledSavingContinuesEvaluationAnd":
    "Cette règle est activée; l’enregistrement poursuit l’évaluation et peut produire des notifications.",
  "system.saveRule": "Enregistrer la règle",
  "system.cancel": "Annuler",
  "system.eventRefreshFailedShowingPreviouslyReadData":
    "Échec de l’actualisation des événements; affichage des données lues précédemment",
  "system.latest50Events": "50 derniers événements",
  "system.refreshEvents": "Actualiser les événements",
  "system.noAlertEventsYet": "Aucun événement d’alerte pour l’instant",
  "system.disabledRulesAndUnknownOrStaleDataDo":
    "Les règles désactivées et les données inconnues ou périmées ne génèrent pas d’événements de rétablissement.",
  "system.eventSample": "Échantillon de l’événement",
  "system.sampled": "Échantillonné",
  "system.eventThresholds": "Seuils de l’événement",
  "system.evidenceSince": "Données depuis",
  "system.hideDeliveryDetails": "Masquer les détails de livraison",
  "system.viewDeliveryDetails": "Afficher les détails de livraison",
  "system.emailDeliveryDetails": "Détails de livraison du courriel",
  "system.deliveryDetailsAreOutOfDate": "Les détails de livraison sont périmés",
  "system.attempts": "Tentatives",
  "system.sMTPAcceptedOnlyMeansTheServerAcceptedThe":
    "«\u00a0Accepté par SMTP\u00a0» signifie seulement que le serveur a accepté le message; cela ne confirme pas la livraison dans la boîte de réception.",
  "system.possibleDuplicate": "Doublon possible",
  "system.anEarlierDeliveryOutcomeWasUncertainARetry":
    "Le résultat d’une livraison antérieure était incertain; une nouvelle tentative peut dupliquer le courriel.",
  "system.nextAttempt": "Prochaine tentative",
  "system.acceptedAt": "Accepté à",
  "system.template": "Modèle",
  "system.confirmEnablingThisRule": "Confirmer l’activation de cette règle",
  "system.confirmDisablingThisRule":
    "Confirmer la désactivation de cette règle",
  "system.confirmDeletingThisRule": "Confirmer la suppression de cette règle",
  "system.localeUnknown": "Langue inconnue",
  "system.enablingMaySendEmailToThisTargetFor":
    "L’activation peut envoyer des courriels à cette cible pour les futurs événements de quota réels. Les événements non mis en file jusque-là ne sont pas rattrapés, et aucun courriel de test n’est envoyé.",
  "system.disablingStopsNewEvaluationsAndRetainsSeverityAnd":
    "La désactivation arrête les nouvelles évaluations et conserve la gravité et l’historique. Les livraisons automatiques non prises en charge sont annulées; une livraison déjà prise en charge peut se terminer une fois.",
  "system.deletionStopsNewEvaluationsAndRetainsHistoryWithout":
    "La suppression arrête les nouvelles évaluations et conserve l’historique sans générer d’événement de rétablissement. Les livraisons automatiques non prises en charge sont annulées; une livraison déjà prise en charge peut se terminer une fois.",
  "system.confirmEnable": "Confirmer l’activation",
  "system.confirmDisable": "Confirmer la désactivation",
  "system.confirmDelete": "Confirmer la suppression",
  "system.ruleState": "État de la règle",
  "system.enabled": "Activée",
  "system.severityIsRetainedCurrentEvidenceCannotDetermineWhether":
    "La gravité est conservée; les données actuelles ne permettent pas de déterminer si le dépôt s’est rétabli.",
  "system.latestSample": "Dernier échantillon",
  "system.historicalSample": "Échantillon historique",
  "system.byteValuesAreApproximateSeverityComesFromThe":
    "Les valeurs en octets sont approximatives; la gravité provient du serveur.",
  "system.noValidCapacitySampleYet":
    "Aucun échantillon de capacité valide pour l’instant",
  "system.warningCondition": "Condition d’avertissement",
  "system.atOrAbove": "Égal ou supérieur à",
  "system.seconds": "secondes",
  "system.criticalCondition": "Condition critique",
  "system.recoveryCondition": "Condition de rétablissement",
  "system.strictlyBelow": "Strictement inférieur à",
  "system.maximumSampleAge": "Âge maximal de l’échantillon",
  "system.lastEvaluation": "Dernière évaluation",
  "system.notEvaluatedYet": "Pas encore évaluée",
  "system.warningEvidenceSince": "Données d’avertissement depuis",
  "system.criticalEvidenceSince": "Données critiques depuis",
  "system.recoveryEvidenceSince": "Données de rétablissement depuis",
  "system.notificationTarget": "Cible de notification",
  "system.recipientConfigured": "Destinataire configuré",
  "system.recipientMissingOrTargetUnavailable":
    "Destinataire manquant ou cible indisponible",
  "system.targetState": "État de la cible",
  "system.unavailable": "Indisponible",
  "system.boundTargetVersion": "Version de cible liée",
  "system.currentTargetVersion": "Version actuelle de la cible",
  "system.theTargetVersionChangedEditAndSaveTo":
    "La version de la cible a changé. Modifiez et enregistrez pour la lier de nouveau; les événements non mis en file jusque-là ne sont pas rattrapés.",
  "system.disableRule": "Désactiver la règle",
  "system.enableRule": "Activer la règle",
  "system.deleteRule": "Supprimer la règle",
  "system.onlyPlatformAdministratorsCanManageQuotaAlerts":
    "Seuls les administrateurs de la plateforme peuvent gérer les alertes de quota",
  "system.repositoryQuotaAlerts": "Alertes de quota des dépôts",
  "system.observesLogicalRepositoryUsageNotThePhysicalCapacity":
    "Surveille l’utilisation logique des dépôts, et non la capacité physique de S3, du NAS ou des montages locaux.",
  "system.refresh": "Actualiser",
  "system.createRule": "Créer une règle",
  "system.showingPreviouslyReadData": "Affichage des données lues précédemment",
  "system.readAt": "Lu à",
  "system.ruleDeletedHistoryRemainsAvailable":
    "Règle supprimée; l’historique reste disponible",
  "system.ruleSaved": "Règle enregistrée",
  "system.disabledRulesCanBeSavedAnAdministratorMust":
    "Les règles désactivées peuvent être enregistrées. Un administrateur doit configurer le canal de courriel et le chiffrement avant de les activer.",
  "system.noEmailTargets": "Aucune cible de courriel",
  "system.createATargetInTheSystemRuntimeEmail":
    "Créez une cible dans l’onglet Notifications par courriel de l’exécution du système, puis revenez ici pour configurer une règle.",
  "system.backToRules": "Retour aux règles",
  "system.noQuotaAlertRules": "Aucune règle d’alerte de quota",
  "system.newRulesAreDisabledByDefaultCreatingOne":
    "Les nouvelles règles sont désactivées par défaut. En créer une n’envoie aucun courriel de test.",
  "system.recoveryBelow": "Rétablissement sous",
  "system.view": "Afficher {name}",
  "system.view2": "Afficher",
  "system.targetSettingsChangedRefreshTheLatestTargetAnd":
    "Les paramètres de la cible ont changé. Actualisez la cible la plus récente et confirmez de nouveau.",
  "system.thisTestKeyBelongsToADifferentRequest":
    "Cette clé de test appartient à une autre requête. Annulez, actualisez la cible et confirmez de nouveau.",
  "system.theTargetIsDisabledCancelAndRefreshNo":
    "La cible est désactivée. Annulez et actualisez; aucun envoi n’a été réessayé automatiquement.",
  "system.theTargetChangedCancelRefreshAndConfirmAgain":
    "La cible a changé. Annulez, actualisez et confirmez de nouveau.",
  "system.theTestRateLimitWasReachedRetryThe":
    "La limite de fréquence des tests est atteinte. Réessayez le même test plus tard.",
  "system.theDeliveryQueueIsFullRetryTheSame":
    "La file de livraison est pleine. Réessayez le même test plus tard.",
  "system.encryptionIsUnavailableContactTheDeploymentAdministrator":
    "Le chiffrement est indisponible. Communiquez avec l’administrateur du déploiement.",
  "system.theEmailChannelIsDisabledContactTheDeployment":
    "Le canal de courriel est désactivé. Communiquez avec l’administrateur du déploiement.",
  "system.validationFailedCheckTheNameSingleRecipientAddress":
    "Échec de la validation. Vérifiez le nom, l’adresse unique du destinataire et la langue.",
  "system.theTargetOrEndpointIsUnavailableCancelAnd":
    "La cible ou le point de terminaison est indisponible. Annulez et actualisez.",
  "system.emailAccessDeniedConfirmPlatformAdministratorAccessAnd":
    "Accès au courriel refusé. Confirmez l’accès d’administrateur de la plateforme et actualisez.",
  "system.thisSessionCannotAccessEmailSettingsSignIn":
    "Cette session ne peut pas accéder aux paramètres de courriel. Reconnectez-vous.",
  "system.theSessionExpiredSignInAgain":
    "La session a expiré. Reconnectez-vous.",
  "system.couldNotReadEmailNotificationDataRetryThe":
    "Impossible de lire les données de notification par courriel. Réessayez la lecture.",
  "system.couldNotConfirmTheSaveRefreshAndCheck":
    "Impossible de confirmer l’enregistrement. Actualisez et vérifiez les paramètres de la cible avant de réessayer.",
  "system.couldNotGenerateTheTemplatePreviewRetryThe":
    "Impossible de générer l’aperçu du modèle. Réessayez l’aperçu; cette opération n’envoie aucun courriel.",
  "system.theRequestFailedAndItsResultMayBe":
    "La requête a échoué et son résultat peut être inconnu. Les nouvelles tentatives de test utilisent la même clé; actualisez pour vérifier les résultats de livraison.",
  "system.theReadRequestWasInvalidRefreshAndCheck":
    "La requête de lecture était invalide. Actualisez et vérifiez la version de Gateway.",
  "system.targetValidationFailedCheckTheNameSingleRecipient":
    "Échec de la validation de la cible. Vérifiez le nom, l’adresse unique du destinataire et la langue.",
  "system.previewValidationFailedCheckTheScenarioAndLanguage":
    "Échec de la validation de l’aperçu. Vérifiez le scénario et la langue.",
  "system.testValidationFailedCheckTheTargetScenarioAnd":
    "Échec de la validation du test. Vérifiez la cible, le scénario et la langue.",
  "system.targetSettingsChangedCancelThisTestRefreshThe":
    "Les paramètres de la cible ont changé. Annulez ce test, actualisez la cible et confirmez de nouveau.",
  "system.readsAreRateLimitedRetryTheReadLater":
    "Les lectures sont limitées en fréquence. Réessayez la lecture plus tard.",
  "system.emailSettingsChangedRetryTheRead":
    "Les paramètres de courriel ont changé. Réessayez la lecture.",
  "system.emailResourcesOrEndpointsAreUnavailableRetryThe":
    "Les ressources ou points de terminaison de courriel sont indisponibles. Réessayez la lecture et vérifiez la version de Gateway.",
  "system.saveRequestsAreRateLimitedRetryLater":
    "Les requêtes d’enregistrement sont limitées en fréquence. Réessayez plus tard.",
  "system.theEmailTargetWasDisabledRefreshAndCheck":
    "La cible de courriel a été désactivée. Actualisez et vérifiez les paramètres de la cible.",
  "system.theEmailTargetChangedRefreshAndConfirmIts":
    "La cible de courriel a changé. Actualisez et confirmez de nouveau ses paramètres.",
  "system.previewRequestsAreRateLimitedRetryThePreview":
    "Les requêtes d’aperçu sont limitées en fréquence. Réessayez l’aperçu plus tard.",
  "system.theTemplatePreviewEndpointIsUnavailableRefreshAnd":
    "Le point de terminaison d’aperçu du modèle est indisponible. Actualisez et vérifiez la version de Gateway.",
  "system.enterATargetName": "Saisissez un nom de cible.",
  "system.useA1128CharacterNameWithoutLine":
    "Utilisez un nom de 1 à 128 caractères sans saut de ligne ni caractère nul.",
  "system.enterOneRecipientEmailAddress":
    "Saisissez l’adresse courriel d’un destinataire.",
  "system.enterOneValidMailboxWithoutADisplayName":
    "Saisissez une seule boîte aux lettres valide, sans nom d’affichage, séparateur ni saut de ligne.",
  "system.editEmailTarget": "Modifier la cible de courriel",
  "system.createDisabledEmailTarget": "Créer une cible de courriel désactivée",
  "system.targetName": "Nom de la cible",
  "system.useATeamOrPurposeNameDoNot":
    "Utilisez un nom d’équipe ou d’objectif; ne mettez pas d’adresse dans le nom.",
  "system.emailLanguage": "Langue du courriel",
  "system.recipientAddress": "Adresse du destinataire",
  "system.theExistingAddressIsWriteOnlyLeaveEmpty":
    "L’adresse existante est en écriture seule. Laissez vide pour la conserver ou saisissez-en une nouvelle; l’annulation ou un conflit efface la saisie.",
  "system.oneRecipientOnlyItsAddressIsNeverReturned":
    "Un seul destinataire; son adresse n’est jamais renvoyée. Les nouvelles cibles sont désactivées.",
  "system.savingDoesNotEnableTheTargetOrSend":
    "L’enregistrement n’active pas la cible et n’envoie aucun courriel.",
  "system.refreshLatestTargetAndEditAgain":
    "Actualiser la cible la plus récente et modifier de nouveau",
  "system.saveTarget": "Enregistrer la cible",
  "system.saveDisabledTarget": "Enregistrer la cible désactivée",
  "system.confirmSyntheticTestSend": "Confirmer l’envoi du test synthétique",
  "system.confirmEnablingEmailTarget":
    "Confirmer l’activation de la cible de courriel",
  "system.confirmDisablingEmailTarget":
    "Confirmer la désactivation de la cible de courriel",
  "system.target": "Cible",
  "system.identityAndConfirmedVersion": "Identité et version confirmée",
  "system.emailLanguageAndRecipient": "Langue du courriel et destinataire",
  "system.oneConfiguredRecipientTheAddressCannotBeRead":
    "Un destinataire configuré; l’adresse ne peut pas être lue",
  "system.syntheticScenario": "Scénario synthétique",
  "system.confirmationSendsOneEmailToThisTargetS":
    "La confirmation envoie un courriel au destinataire configuré de cette cible",
  "system.theDataIsSyntheticTheRequestEntersThe":
    "Les données sont synthétiques. La requête entre dans la vraie file de livraison et est limitée en fréquence. L’annulation ne peut pas rappeler un courriel en file ou en cours d’envoi. L’acceptation SMTP ne signifie pas la livraison dans la boîte de réception.",
  "system.retryKeepsThisTargetVersionScenarioAndTest":
    "La nouvelle tentative conserve cette cible, cette version, ce scénario et cette clé de test; elle ne crée pas automatiquement un autre test.",
  "system.enablingSendsNoTestEmail":
    "L’activation n’envoie aucun courriel de test",
  "system.disablingCannotRecallInFlightMail":
    "La désactivation ne peut pas rappeler les courriels en cours d’envoi",
  "system.theTargetVersionChangesExistingQuotaRulesBind":
    "La version de la cible change. Les règles de quota existantes sont liées à l’ancienne version et exigent une confirmation de l’acheminement sur la page des règles; les courriels en file de l’ancienne version seront arrêtés.",
  "system.confirmSyntheticTestSend2": "Confirmer l’envoi du test synthétique",
  "system.emailTemplatePreview": "Aperçu du modèle de courriel",
  "system.offlineSyntheticSampleNoRecipientReadQueueEntry":
    "Échantillon synthétique hors ligne\u00a0: aucune lecture de destinataire, mise en file ni envoi. Les liens et les ressources externes sont désactivés dans l’aperçu en bac à sable.",
  "system.previewScenario": "Scénario de l’aperçu",
  "system.previewLanguage": "Langue de l’aperçu",
  "system.previewTemplate": "Modèle de l’aperçu",
  "system.previewFormat": "Format de l’aperçu",
  "system.plainText": "Texte brut",
  "system.templateVersion": "Version du modèle",
  "system.syntheticEmailHTMLPreview": "Aperçu HTML du courriel synthétique",
  "system.chooseAScenarioAndPreviewTheTemplate":
    "Choisissez un scénario et affichez l’aperçu du modèle",
  "system.warningCriticalAndRecoveryProvideEnglishAndChinese":
    "Les scénarios d’avertissement, critique et de rétablissement fournissent du HTML en anglais, en français et en chinois, ainsi qu’un texte brut équivalent.",
  "system.recentDeliveryResultsUpTo50":
    "Résultats de livraison récents (jusqu’à 50)",
  "system.sMTPAcceptanceDoesNotMeanInboxDeliveryOr":
    "L’acceptation SMTP ne signifie pas la livraison dans la boîte de réception ni la lecture. La désactivation ne peut pas rappeler les courriels en cours d’envoi; les nouvelles tentatives incertaines peuvent dupliquer des courriels.",
  "system.noEmailDeliveriesYet": "Aucune livraison de courriel pour l’instant",
  "system.repositoryQuotaAlert": "Alerte de quota du dépôt",
  "system.administratorSyntheticTest": "Test synthétique d’administrateur",
  "system.deliveryID": "ID de livraison",
  "system.updated": "Mis à jour",
  "system.nextAttempt2": "Prochaine tentative",
  "system.accepted": "Accepté",
  "system.possibleDuplicateAnEarlierSMTPAttemptHadAn":
    "Doublon possible\u00a0: une tentative SMTP antérieure avait un résultat incertain.",
  "system.onlyPlatformAdministratorsCanManageEmailNotifications":
    "Seuls les administrateurs de la plateforme peuvent gérer les notifications par courriel",
  "system.emailNotifications": "Notifications par courriel",
  "system.manageWriteOnlyRecipientTargetsPreviewTemplatesAnd":
    "Gérez les cibles de destinataires en écriture seule, affichez l’aperçu des modèles et envoyez explicitement des tests synthétiques.",
  "system.syntheticTestQueuedCheckDeliveryResults":
    "Test synthétique mis en file; vérifiez les résultats de livraison",
  "system.emailTargetSavedNoTestSent":
    "Cible de courriel enregistrée; aucun test envoyé",
  "system.aDeploymentAdministratorConfiguresTheChannelThisPage":
    "Un administrateur du déploiement configure le canal; cette page n’accepte aucun justificatif d’identité SMTP. L’aperçu reste disponible; l’enregistrement des cibles exige un chiffrement utilisable.",
  "system.emailTargets": "Cibles de courriel",
  "system.newEmailTarget": "Nouvelle cible de courriel",
  "system.newTargetsAreDisabledSavingSendsNoMail":
    "Les nouvelles cibles sont désactivées; l’enregistrement n’envoie aucun courriel.",
  "system.recipientConfiguredWriteOnly":
    "Destinataire configuré (écriture seule)",
  "system.recipientNotConfigured": "Destinataire non configuré",
  "system.edit": "Modifier {name}",
  "system.edit2": "Modifier",
  "system.disable": "Désactiver",
  "system.enable": "Activer",
  "system.test": "Tester {name}",
  "system.sendSyntheticTest": "Envoyer un test synthétique…",
  "system.refreshesEvery30SecondsWhileVisible":
    "Actualisation toutes les 30 secondes tant que la page est visible",
  "system.systemRuntime": "Exécution du système",
  "system.inspectGatewayNodesDiagnosticsLogsRepositoryQuotaAlerts":
    "Inspectez les nœuds Gateway, les diagnostics, les journaux, les alertes de quota des dépôts et les notifications par courriel.",
  "system.systemDiagnostics": "Diagnostics du système",
  "system.runtimeLogs": "Journaux d’exécution",
  "system.quotaAlerts": "Alertes de quota",
  "system.warningThreshold": "Seuil d’avertissement (%)",
  "system.warningHoldDuration":
    "Durée de maintien de l’avertissement (secondes)",
  "system.criticalThreshold": "Seuil critique (%)",
  "system.criticalHoldDuration":
    "Durée de maintien du seuil critique (secondes)",
  "system.recoveryThreshold": "Seuil de rétablissement (%)",
  "system.recoveryHoldDuration":
    "Durée de maintien du rétablissement (secondes)",
  "system.disableTarget": "Désactiver {name}",
  "system.enableTarget": "Activer {name}",
  "system.theTargetIsEnabledSavingRetainsThisState":
    "La cible est activée; l’enregistrement conserve cet état et n’envoie aucun test.",
  "system.theTargetIsDisabledSavingRetainsThisState":
    "La cible est désactivée; l’enregistrement conserve cet état et n’envoie aucun test.",
};
