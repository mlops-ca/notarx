import type { identityEnCA } from "./identity.en-CA";

export const identityFrCA: Record<keyof typeof identityEnCA, string> = {
  "identity.copyToken": "Copier le token",
  "identity.copied": "Copié",
  "identity.newKey": "Nouvelle clé",
  "identity.newApiKey": "Nouvelle clé d’API",
  "identity.cancel": "Annuler",
  "identity.create": "Créer",
  "identity.keyName": "Nom de la clé",
  "identity.describeItsPurposeForExampleCiDeploy":
    "Décrivez son objet, par exemple ci-deploy",
  "identity.role": "Rôle",
  "identity.chooseTheLeastPrivilegeThisGlobalRoleAppliesAcross":
    "Choisissez le moindre privilège. Ce rôle global s’applique à tous les dépôts avant les autorisations de dépôt.",
  "identity.memberNoGlobalAccessRepositoryGrantsOnly":
    "member · aucun accès global, autorisations de dépôt seulement",
  "identity.adminAllPermissions": "admin · toutes les permissions",
  "identity.validity": "Validité",
  "identity.authenticationIsRejectedAutomaticallyAfterExpiry":
    "L’authentification est refusée automatiquement après l’expiration.",
  "identity.30Days": "30 jours",
  "identity.90DaysRecommended": "90 jours (recommandé)",
  "identity.180Days": "180 jours",
  "identity.365Days": "365 jours",
  "identity.thePlaintextTokenIsShownOnceSaveItImmediately":
    "Le token en clair n’est affiché qu’une seule fois. Enregistrez-le immédiatement.",
  "identity.name": "Nom",
  "identity.roles": "Rôles",
  "identity.status": "Statut",
  "identity.created": "Créée",
  "identity.expires": "Expire",
  "identity.lastUsed": "Dernière utilisation",
  "identity.actions": "Actions",
  "identity.revokeKey": "Révoquer la clé {key}",
  "identity.apiKeys": "Clés d’API",
  "identity.manageMemberAndAdminCredentialsForManagementApis":
    "Gérez les justificatifs d’identité member et admin pour les API de gestion",
  "identity.noApiKeys": "Aucune clé d’API",
  "identity.createAKeyForScriptsAndCiToCall":
    "Créez une clé pour que les scripts et la CI appellent les API de gestion",
  "identity.activeKeys": "Clés actives",
  "identity.canCallManagementApis": "Peuvent appeler les API de gestion",
  "identity.adminKeys": "Clés admin",
  "identity.rotateAndReviewRegularly":
    "À renouveler et à examiner régulièrement",
  "identity.noElevatedKeys": "Aucune clé privilégiée",
  "identity.revokedExpired": "Révoquées / expirées",
  "identity.retainedForAuditHistory": "Conservées pour l’historique d’audit",
  "identity.search": "Rechercher",
  "identity.searchNamesOrRoles": "Rechercher des noms ou des rôles…",
  "identity.allStatuses": "Tous les statuts",
  "identity.active": "Active",
  "identity.expired": "Expirée",
  "identity.revoked": "Révoquée",
  "identity.noMatchingKeys": "Aucune clé correspondante",
  "identity.adjustTheFiltersAndTryAgain": "Ajustez les filtres et réessayez",
  "identity.apiKeyCreatedSaveTheTokenNow":
    "Clé d’API créée\u00a0: enregistrez le token maintenant",
  "identity.thisIsTheOnlyPlaintextTokenDisplay":
    "Ceci est la seule fois où le token est affiché en clair",
  "identity.itCannotBeViewedAgainAfterClosingStoreIt":
    "Il ne pourra plus être consulté après la fermeture. Conservez-le en lieu sûr maintenant.",
  "identity.iSavedIt": "Je l’ai enregistré",
  "identity.revokeApiKey": "Révoquer la clé d’API",
  "identity.revokeKey2": "Révoquer la clé",
  "identity.itsTokenBecomesInvalidImmediately":
    "? Son token devient invalide immédiatement.",
  "identity.revoke": "Révoquer",
  "identity.credential": "Justificatif d’identité",
  "identity.state": "État",
  "identity.revoke2": "Révoquer {credential}",
  "identity.noPurposeDocumented": "Aucun objet documenté",
  "identity.refresh": "Actualiser",
  "identity.disableAccount": "Désactiver le compte",
  "identity.enableAccount": "Activer le compte",
  "identity.stableGrantPrincipal": "Principal d’autorisation stable",
  "identity.rotatingCredentials": "Justificatifs d’identité renouvelés",
  "identity.issueCredential": "Émettre un justificatif d’identité",
  "identity.loadingCredentials": "Chargement des justificatifs d’identité…",
  "identity.noCredentials": "Aucun justificatif d’identité",
  "identity.issueTheFirstShortLivedCredentialAndStoreIt":
    "Émettez le premier justificatif d’identité de courte durée et conservez-le dans un secret de CI.",
  "identity.loadMoreCredentials": "Charger plus de justificatifs d’identité",
  "identity.creationFailed": "Échec de la création",
  "identity.newServiceAccount": "Nouveau compte de service",
  "identity.accountName": "Nom du compte",
  "identity.forExamplePipeoneCiOrReleaseBot":
    "Par exemple pipeone-ci ou release-bot",
  "identity.purpose": "Objet",
  "identity.issuanceFailed": "Échec de l’émission",
  "identity.issue": "Émettre",
  "identity.credentialName": "Nom du justificatif d’identité",
  "identity.includeTheConsumerAndRotationBatch":
    "Indiquez le consommateur et le lot de renouvellement",
  "identity.oldAndNewCredentialsMayOverlapBrieflyDeployThe":
    "Les anciens et les nouveaux justificatifs d’identité peuvent coexister brièvement\u00a0: déployez le nouveau token, vérifiez-le, puis révoquez l’ancien pour un renouvellement sans interruption.",
  "identity.serviceAccounts": "Comptes de service",
  "identity.giveJenkinsCiRobotsAndThirdPartyApplicationsA":
    "Donnez à Jenkins, aux robots de CI et aux applications tierces une identité stable pendant que les justificatifs d’identité sont renouvelés indépendamment.",
  "identity.machineIdentityIsSeparateFromCredentials":
    "L’identité machine est distincte des justificatifs d’identité",
  "identity.repositoryGrantsBindToTheStableServiceAccountId":
    "Les autorisations de dépôt sont liées au principal stable service-account:<id>. Le renouvellement des tokens ne modifie pas les autorisations, et la désactivation du compte rejette tous les justificatifs d’identité à la fois.",
  "identity.stableMachinePrincipals": "Principals machine stables",
  "identity.active2": "Actifs",
  "identity.mayAcceptCredentialAuth":
    "Peuvent accepter l’authentification par justificatif d’identité",
  "identity.activeCredentials": "Justificatifs d’identité actifs",
  "identity.supportsZeroDowntimeRotation":
    "Permet un renouvellement sans interruption",
  "identity.loadingServiceAccounts": "Chargement des comptes de service…",
  "identity.noServiceAccounts": "Aucun compte de service",
  "identity.createAStableIdentityForYourFirstCiRobot":
    "Créez une identité stable pour votre premier robot de CI.",
  "identity.machinePrincipals": "Principals machine",
  "identity.loadMoreAccounts": "Charger plus de comptes",
  "identity.credentialIssuedSaveTheToken":
    "Justificatif d’identité émis\u00a0: enregistrez le token",
  "identity.plaintextIsShownOnce":
    "Le texte en clair n’est affiché qu’une seule fois",
  "identity.itCannotBeViewedAgainPutTheTokenIn":
    "Il ne pourra plus être consulté. Placez le token dans un secret de CI ou un gestionnaire de secrets externe, jamais dans le contrôle de source.",
  "identity.expires2": "Expire",
  "identity.iStoredItSecurely": "Je l’ai conservé en lieu sûr",
  "identity.revokeCredential": "Révoquer le justificatif d’identité",
  "identity.afterRevokingJobsUsingThatTokenFailAuthenticationImmediately":
    "Après la révocation de {credentialToRevoke}, les tâches qui utilisent ce token échouent immédiatement à l’authentification. Les autres justificatifs d’identité restent valides.",
  "identity.revoke3": "Révoquer",
  "identity.disableServiceAccount": "Désactiver le compte de service",
  "identity.enableServiceAccount": "Activer le compte de service",
  "identity.everyExistingCredentialStopsAuthenticatingImmediatelyGrantsAndAudit":
    "Tous les justificatifs d’identité existants cessent immédiatement de s’authentifier. Les autorisations et l’identité d’audit sont conservées, et les justificatifs d’identité non expirés refonctionnent après la réactivation.",
  "identity.nonExpiredNonRevokedCredentialsWillAuthenticateAgain":
    "Les justificatifs d’identité non expirés et non révoqués s’authentifieront de nouveau.",
  "identity.disable": "Désactiver",
  "identity.enable": "Activer",
  "identity.user": "Utilisateur",
  "identity.pending": "En attente",
  "identity.access": "Accès",
  "identity.locked": "Verrouillé",
  "identity.lastSignIn": "Dernière connexion",
  "identity.neverSignedIn": "Jamais connecté",
  "identity.password": "Mot de passe",
  "identity.ssoOnly": "SSO seulement",
  "identity.changeRequired": "Changement requis",
  "identity.ready": "Prêt",
  "identity.changed": "Modifié",
  "identity.viewAndManage": "Afficher et gérer",
  "identity.userActions": "Actions sur l’utilisateur",
  "identity.manageUser": "Gérer l’utilisateur {user}",
  "identity.users": "Utilisateurs",
  "identity.manageLocalAndSsoAccountsSignInStatusRoles":
    "Gérez les comptes locaux et SSO, l’état de connexion, les rôles et les sessions actives.",
  "identity.newUser": "Nouvel utilisateur",
  "identity.searchUsernameDisplayNameOrEmail":
    "Rechercher un nom d’utilisateur, un nom d’affichage ou un courriel…",
  "identity.allRoles": "Tous les rôles",
  "identity.awaitingApproval": "En attente d’approbation",
  "identity.accountState": "État du compte",
  "identity.allStates": "Tous les états",
  "identity.disabled": "Désactivé",
  "identity.noMatchingUsers": "Aucun utilisateur correspondant",
  "identity.noUsers": "Aucun utilisateur",
  "identity.createTheFirstLocalAccount": "Créez le premier compte local",
  "identity.failedToSaveUser": "Échec de l’enregistrement de l’utilisateur",
  "identity.userProfileSaved": "Profil de l’utilisateur enregistré",
  "identity.failedToResetPassword":
    "Échec de la réinitialisation du mot de passe",
  "identity.passwordResetAndExistingSessionsRevoked":
    "Mot de passe réinitialisé et sessions existantes révoquées",
  "identity.failedToRevokeSessions": "Échec de la révocation des sessions",
  "identity.allSessionsRevoked": "Toutes les sessions ont été révoquées",
  "identity.userDeleted": "Utilisateur supprimé",
  "identity.close": "Fermer",
  "identity.saveChanges": "Enregistrer les modifications",
  "identity.accountTemporarilyLockedAfterFailedSignIns":
    "Compte temporairement verrouillé après des échecs de connexion",
  "identity.theUserMustChangeTheirPasswordAtNextSign":
    "L’utilisateur doit changer son mot de passe à la prochaine connexion",
  "identity.lockedUntil": "Verrouillé jusqu’au {locale}",
  "identity.accountProfile": "Profil du compte",
  "identity.displayName": "Nom d’affichage",
  "identity.email": "Courriel",
  "identity.enterAValidEmail": "Saisissez un courriel valide",
  "identity.description": "Description",
  "identity.memberNoRepositoryAccessUntilGranted":
    "member · aucun accès aux dépôts tant qu’il n’est pas autorisé",
  "identity.noneAwaitingApproval": "none · en attente d’approbation",
  "identity.adminAdministrator": "admin · administrateur",
  "identity.signInAndSecurity": "Connexion et sécurité",
  "identity.never": "Jamais",
  "identity.failedAttempts": "Tentatives échouées",
  "identity.passwordChanged": "Mot de passe modifié",
  "identity.notConfigured": "Non configuré",
  "identity.localPassword": "Mot de passe local",
  "identity.enabled": "Activé",
  "identity.notEnabledExternalIdentity": "Non activé (identité externe)",
  "identity.changeRequired2": "Changement requis",
  "identity.yes": "Oui",
  "identity.no": "Non",
  "identity.updated": "Mis à jour",
  "identity.resetPassword": "Réinitialiser le mot de passe",
  "identity.setLocalPassword": "Définir le mot de passe local",
  "identity.revokeAllSessions": "Révoquer toutes les sessions",
  "identity.deleteAccount": "Supprimer le compte",
  "identity.deletionCannotBeUndoneTheFinalActiveAdministratorIs":
    "La suppression est irréversible. Le dernier administrateur actif est protégé.",
  "identity.deleteUser": "Supprimer l’utilisateur",
  "identity.resetPasswordFor": "Réinitialiser le mot de passe de {user}",
  "identity.newPassword": "Nouveau mot de passe",
  "identity.enterANewPassword": "Saisissez un nouveau mot de passe",
  "identity.useAtLeast8Characters": "Utilisez au moins 8 caractères",
  "identity.useNoMoreThan72Bytes": "Utilisez au plus 72 octets",
  "identity.nextSignIn": "Prochaine connexion",
  "identity.changeRequired3": "Changement requis",
  "identity.readyToUse": "Prêt à l’emploi",
  "identity.revokeSessions": "Révoquer les sessions",
  "identity.permanentlyDeleteTheAccountImmediatelyLosesAccess":
    "Supprimer définitivement l’utilisateur «\u00a0{user}\u00a0»? Le compte perd immédiatement son accès.",
  "identity.allLocalSessionTokensIssuedToBecomeInvalidImmediately":
    "Tous les tokens de session locale émis pour {user} deviennent invalides immédiatement.",
  "identity.thisPanelOnlyManagesGrantsForThisUser":
    "Ce panneau gère seulement les autorisations de cet utilisateur",
  "identity.selectARepositoryAndWaitForItsGrantsTo":
    "Sélectionnez un dépôt et attendez le chargement de ses autorisations",
  "identity.thePreviousResourceScopeCouldNotBeRemovedSo":
    "La portée de ressource précédente n’a pas pu être supprimée; la nouvelle autorisation n’a donc pas été enregistrée. Veuillez réessayer.",
  "identity.thePreviousRowWasRemovedButSavingTheNew":
    "La ligne précédente a été supprimée, mais l’enregistrement de la nouvelle autorisation a échoué. Enregistrez de nouveau pour l’écrire.",
  "identity.repositoryGrantSaved": "Autorisation de dépôt enregistrée",
  "identity.addRepositoryGrant": "Ajouter une autorisation de dépôt",
  "identity.editRepositoryGrant": "Modifier l’autorisation de dépôt",
  "identity.save": "Enregistrer",
  "identity.repository": "Dépôt",
  "identity.selectARepositoryToGrant": "Sélectionnez un dépôt à autoriser",
  "identity.customRolesAreUnavailableBuiltInPermissionsStillWork":
    "Les rôles personnalisés sont indisponibles; les permissions intégrées fonctionnent toujours.",
  "identity.repositoryRulesAddPermissionsTheyCannotRevokeTheUser":
    "Les règles de dépôt ajoutent des permissions; elles ne peuvent pas révoquer le rôle global existant de l’utilisateur. Les autres portées de ressource de l’utilisateur et les autres principals conservent les leurs.",
  "identity.thisUserAlreadyHoldsAGrantAtThisResource":
    "Cet utilisateur détient déjà une autorisation à cette portée de ressource; l’enregistrement la remplace.",
  "identity.pickARepositoryThenConfigureThisUserSPermission":
    "Choisissez un dépôt, puis configurez le niveau de permission et la portée de ressource de cet utilisateur.",
  "identity.failedToLinkIdentity": "Échec de la liaison de l’identité",
  "identity.identityLinked": "Identité liée",
  "identity.identityUnlinked": "Identité dissociée",
  "identity.externalIdentities": "Identités externes",
  "identity.linkedOidcSignInsUseThisAccountSState":
    "Les connexions OIDC liées utilisent l’état, le rôle et la politique de révocation de session de ce compte.",
  "identity.refreshExternalIdentities": "Actualiser les identités externes",
  "identity.linkIdentity": "Lier une identité",
  "identity.noExternalIdentityLinked": "Aucune identité externe liée",
  "identity.emailVerified": "Courriel vérifié",
  "identity.lastSignIn2": "Dernière connexion",
  "identity.never2": "Jamais",
  "identity.unlinkThisIdentity": "Dissocier cette identité?",
  "identity.thisOidcIdentityWillNoLongerEnterThisAccount":
    "Cette identité OIDC ne pourra plus accéder à ce compte.",
  "identity.unlink": "Dissocier",
  "identity.unlinkIdentity": "Dissocier l’identité",
  "identity.linkOidcIdentity": "Lier une identité OIDC",
  "identity.link": "Lier",
  "identity.theIdentityProviderIsReadFromTheCurrentOidc":
    "Le fournisseur d’identité est lu à partir de la configuration OIDC actuelle.",
  "identity.issuer": "Émetteur",
  "identity.noOidcProviderIsConfiguredSoIdentitiesCannotBe":
    "Aucun fournisseur OIDC n’est configuré; les identités ne peuvent donc pas encore être liées.",
  "identity.enterTheSubClaimFromTheOidcIdToken":
    "Saisissez la revendication sub du token d’ID OIDC, l’identifiant d’utilisateur unique et stable du fournisseur.",
  "identity.forExample00u123": "Par exemple\u00a0: 00u123…",
  "identity.repositoryGrantRemoved": "Autorisation de dépôt supprimée",
  "identity.repositoryAccess": "Accès aux dépôts",
  "identity.grantThisUserReadWriteAdminOrArtifactIntelligence":
    "Accordez à cet utilisateur un accès en lecture, en écriture, d’administration ou d’intelligence des artefacts à des dépôts individuels, éventuellement limité à une portée de ressource. Les rôles globaux ne sont pas touchés.",
  "identity.refreshRepositoryGrants": "Actualiser les autorisations de dépôt",
  "identity.addGrant": "Ajouter une autorisation",
  "identity.theRepositoryListIsUnavailableSoNewGrantsCannot":
    "La liste des dépôts est indisponible; il est donc impossible d’ajouter de nouvelles autorisations. Les autorisations existantes peuvent toujours être consultées et supprimées.",
  "identity.thisUserHasNoRepositoryGrants":
    "Cet utilisateur n’a aucune autorisation de dépôt",
  "identity.useAddGrantToPickARepositoryAndConfigure":
    "Utilisez «\u00a0Ajouter une autorisation\u00a0» pour choisir un dépôt et configurer les permissions. L’autorisation prend effet immédiatement.",
  "identity.entireRepository": "Dépôt entier",
  "identity.editThisGrant": "Modifier cette autorisation",
  "identity.removeThisUserSGrant":
    "Supprimer l’autorisation de cet utilisateur?",
  "identity.afterRemovalEveryRepositoryGrantForOnIsDeleted":
    "Après la suppression, toutes les autorisations de {username} sur «\u00a0{repositoryName}\u00a0» sont supprimées. Les autres utilisateurs ne sont pas touchés.",
  "identity.remove": "Supprimer",
  "identity.removeRepositoryGrant": "Supprimer l’autorisation de dépôt",
  "identity.failedToRevokeSession": "Échec de la révocation de la session",
  "identity.sessionRevoked": "Session révoquée",
  "identity.userSessions": "Sessions de l’utilisateur",
  "identity.revokeAnIndividualClientExpiredHistoryIsRetainedFor":
    "Révoquez un client individuel. L’historique expiré est conservé pendant 30 jours.",
  "identity.showHistory": "Afficher l’historique",
  "identity.showInactiveSessions": "Afficher les sessions inactives",
  "identity.refreshSessions": "Actualiser les sessions",
  "identity.noSessionHistory": "Aucun historique de session",
  "identity.noActiveSessions": "Aucune session active",
  "identity.localSignIn": "Connexion locale",
  "identity.current": "Actuelle",
  "identity.revoked2": "Révoquée",
  "identity.unknownClient": "Client inconnu",
  "identity.address": "Adresse",
  "identity.unknown": "Inconnu",
  "identity.created2": "Créée",
  "identity.revokeThisSession": "Révoquer cette session?",
  "identity.thisIsTheCurrentSessionYouWillNeedTo":
    "Il s’agit de la session actuelle. Vous devrez vous reconnecter.",
  "identity.theCorrespondingClientWillBeSignedOutImmediately":
    "Le client correspondant sera déconnecté immédiatement.",
  "identity.revoke4": "Révoquer",
  "identity.revokeSession": "Révoquer la session",
  "identity.staticAdministratorToken": "Token d’administrateur statique",
  "identity.staticResolverCredential":
    "Justificatif d’identité de résolveur statique",
  "identity.localUserSession": "Session d’utilisateur local",
  "identity.serviceAccountCredential":
    "Justificatif d’identité de compte de service",
  "identity.member": "Member",
  "identity.administrator": "Administrateur",
  "identity.principal": "Principal",
  "identity.authentication": "Authentification",
  "identity.globalRole": "Rôle global",
  "identity.noneRepositoryRulesDecide": "Aucun; les règles de dépôt décident",
  "identity.administrator2": "Administrateur",
  "identity.oIDCMapping": "Correspondance OIDC",
  "identity.administratorSubjectListAdmin":
    "Liste des sujets administrateurs → admin",
  "identity.noConfiguredRoleMappingMatched":
    "Aucune correspondance de rôle configurée",
  "identity.failedToCreateUser": "Échec de la création de l’utilisateur",
  "identity.userCreated": "Utilisateur créé",
  "identity.createLocalUser": "Créer un utilisateur local",
  "identity.createUser": "Créer l’utilisateur",
  "identity.username": "Nom d’utilisateur",
  "identity.enterAUsername": "Saisissez un nom d’utilisateur",
  "identity.forExampleAliceChen": "Par exemple\u00a0: Alice Chen",
  "identity.recordTheAccountPurposeTeamOrResponsibility":
    "Indiquez l’objet du compte, l’équipe ou la responsabilité",
  "identity.initialPassword": "Mot de passe initial",
  "identity.useAtLeast8CharactersAndNoMore":
    "Utilisez au moins 8 caractères et au plus 72 octets. Le mot de passe n’est plus jamais affiché.",
  "identity.enterAnInitialPassword": "Saisissez un mot de passe initial",
  "identity.firstSignInRequirement": "Exigence à la première connexion",
  "identity.viewGrants": "Afficher les autorisations de dépôt",
  "identity.emailVerifiedByAdministrator":
    "Courriel vérifié par un administrateur",
  "identity.emailVerifiedByOn": "Oui, par {actor} le {date}",
  "identity.emailVerificationHelp":
    "Une connexion par authentification unique ne peut lier ce compte par courriel que si un administrateur a vérifié l’adresse. Modifier le courriel annule la vérification.",
  "identity.markEmailVerified": "Marquer le courriel comme vérifié",
  "identity.clearEmailVerification": "Annuler la vérification du courriel",
  "identity.emailVerificationSaved": "Vérification du courriel enregistrée",
  "identity.failedToSaveEmailVerification":
    "Échec de l’enregistrement de la vérification du courriel",
  "identity.viewRecentAudits": "Afficher les audits récents",
};
