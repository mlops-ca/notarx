import type { accessEnCA } from "./access.en-CA";

export const accessFrCA: Record<keyof typeof accessEnCA, string> = {
  "access.accessControl": "Contrôle d’accès",
  "access.evaluateEffectivePermissionsAndReviewAnonymousAndRepositoryAccess":
    "Évaluez les permissions effectives et examinez les règles d’accès anonyme et d’accès aux dépôts",
  "access.grants": "Autorisations ({grants})",
  "access.accessEvaluation": "Évaluation d’accès",
  "access.policiesTemplates": "Politiques et modèles",
  "access.currentIdentity": "Identité actuelle",
  "access.noGlobalRole": "aucun rôle global",
  "access.user": "Utilisateur",
  "access.globalRole": "Rôle global",
  "access.none": "aucun",
  "access.serviceAccount": "Compte de service",
  "access.noGlobalRoleRepositoryGrantsDecide":
    "Aucun rôle global; les autorisations de dépôt décident",
  "access.oidcCustomActor": "OIDC / acteur personnalisé",
  "access.enterTheCompleteAuthenticatedActorAndSelectItsGlobal":
    "Saisissez l’acteur authentifié complet et sélectionnez son rôle global",
  "access.selectAPrincipalAndRepository":
    "Sélectionnez un principal et un dépôt",
  "access.enterTheCompleteActorProducedByTheAuthenticationSystem":
    "Saisissez l’acteur complet produit par le système d’authentification",
  "access.accessEvaluationFailed": "Échec de l’évaluation d’accès",
  "access.runTheGatewayDecisionChainAcrossGlobalRolesRepository":
    "Exécutez la chaîne de décision du Gateway sur les rôles globaux, les autorisations de dépôt et les préfixes de ressource.",
  "access.someIdentitySourcesAreUnavailable":
    "Certaines sources d’identité sont indisponibles",
  "access.serviceAccountsAreTemporarilyUnavailableYouCanStillEvaluate":
    "Les comptes de service sont temporairement indisponibles. Vous pouvez tout de même évaluer l’identité actuelle, les utilisateurs, les clés d’API et les acteurs personnalisés.",
  "access.retry": "Réessayer",
  "access.principal": "Principal",
  "access.none2": "Aucun",
  "access.repository": "Dépôt",
  "access.selectRepository": "Sélectionner un dépôt",
  "access.resourceOptional": "Ressource (facultatif)",
  "access.blankEvaluatesRepositoryWideAccess":
    "Laisser vide pour évaluer l’accès à l’ensemble du dépôt",
  "access.evaluate": "Évaluer",
  "access.completeActorIdentifier": "Identifiant d’acteur complet",
  "access.forExampleOidcGitlabTeamReleaseOrCiBot":
    "Par exemple\u00a0: oidc:gitlab:team/release ou ci-bot",
  "access.thisMustExactlyMatchTheAuthenticatedActorChooseThe":
    "Il doit correspondre exactement à l’acteur authentifié; choisissez le rôle global produit par la correspondance OIDC.",
  "access.simulated": "Simulé",
  "access.currentIdentity2": "Identité actuelle",
  "access.entireRepository": "Dépôt entier",
  "access.authorizationOrderAndRoles": "Ordre d’autorisation et rôles",
  "access.howRulesCombine": "Combinaison des règles",
  "access.repositoryRulesAddPermissionsTheyCannotRevokeAGlobal":
    "Les règles de dépôt ajoutent des permissions; elles ne peuvent pas révoquer un rôle global.",
  "access.failedToLoadRepositoryGrantVersion":
    "Échec du chargement de la version des autorisations du dépôt",
  "access.applyAuthorizationTemplate": "Appliquer le modèle d’autorisation",
  "access.cancel": "Annuler",
  "access.apply": "Appliquer",
  "access.chooseATargetRepositoryApplyingReplacesItsCurrentGrants":
    "Choisissez un dépôt cible. L’application remplace ses autorisations actuelles, avec protection par concurrence optimiste.",
  "access.read": "Lecture",
  "access.write": "Écriture",
  "access.artifactIntelligence": "Intelligence des artefacts",
  "access.admin": "Admin",
  "access.roleNameAndAtLeastOnePermissionAreRequired":
    "Le nom du rôle et au moins une permission sont obligatoires",
  "access.failedToSaveAuthorizationRole":
    "Échec de l’enregistrement du rôle d’autorisation",
  "access.role": "Rôle",
  "access.noDescription": "Aucune description",
  "access.permissions": "Permissions",
  "access.updated": "Mis à jour",
  "access.actions": "Actions",
  "access.editRole": "Modifier le rôle",
  "access.deleteThisRole": "Supprimer ce rôle?",
  "access.savedTemplatesAndRepositoryGrantsWillNotChange":
    "Les modèles enregistrés et les autorisations de dépôt ne changeront pas.",
  "access.delete": "Supprimer",
  "access.deleteRole": "Supprimer le rôle",
  "access.customAuthorizationRoles": "Rôles d’autorisation personnalisés",
  "access.newRole": "Nouveau rôle",
  "access.rolesReusePermissionSetsTemplatesAndGrantsCopyA":
    "Les rôles réutilisent des ensembles de permissions. Les modèles et les autorisations copient un instantané de la portée; les modifications ou la suppression ultérieures d’un rôle ne changent donc jamais l’accès enregistré.",
  "access.noCustomRolesYet": "Aucun rôle personnalisé pour l’instant",
  "access.savePermissionsThatAreCommonlyGrantedTogetherAndReuse":
    "Enregistrez les permissions souvent accordées ensemble et réutilisez-les lors de la modification des modèles.",
  "access.editAuthorizationRole": "Modifier le rôle d’autorisation",
  "access.newAuthorizationRole": "Nouveau rôle d’autorisation",
  "access.save": "Enregistrer",
  "access.roleName": "Nom du rôle",
  "access.forExampleReleaseReviewer": "Par exemple Réviseur de versions",
  "access.description": "Description",
  "access.describeTheResponsibilityThisRoleServes":
    "Décrivez la responsabilité que ce rôle remplit",
  "access.permissions2": "Permissions",
  "access.selectOneOrMorePermissions":
    "Sélectionnez une ou plusieurs permissions",
  "access.teamReadOnly": "Équipe en lecture seule",
  "access.forPrincipalsThatOnlyDownloadSearchAndBrowseArtifacts":
    "Pour les principals qui se contentent de télécharger, de rechercher et de parcourir des artefacts.",
  "access.releasePublisher": "Éditeur de versions",
  "access.forCiPrincipalsThatPublishAndEditArtifacts":
    "Pour les principals de CI qui publient et modifient des artefacts.",
  "access.artifactIntelligenceBot": "Robot d’intelligence des artefacts",
  "access.onlyAllowsSecurityAndArtifactIntelligenceMetadataWrites":
    "Autorise uniquement l’écriture de métadonnées de sécurité et d’intelligence des artefacts.",
  "access.repositoryOwner": "Propriétaire du dépôt",
  "access.forPrincipalsResponsibleForRepositoryConfigurationGrantsAndLifecycle":
    "Pour les principals responsables de la configuration, des autorisations et du cycle de vie du dépôt.",
  "access.template": "Modèle",
  "access.rules": "Règles",
  "access.edit": "Modifier",
  "access.deleteThisTemplate": "Supprimer ce modèle?",
  "access.deleteTemplate": "Supprimer le modèle",
  "access.authorizationTemplates": "Modèles d’autorisation",
  "access.builtInTemplates": "Modèles intégrés",
  "access.newTemplate": "Nouveau modèle",
  "access.templatesStoreReusableRulesOnlyApplyingOneUpdatesA":
    "Les modèles ne stockent que des règles réutilisables. En appliquer un met à jour un dépôt avec des vérifications de version optimistes.",
  "access.noAuthorizationTemplatesYet":
    "Aucun modèle d’autorisation pour l’instant",
  "access.templatesReuseASetOfPrincipalScopeAndResource":
    "Les modèles réutilisent un ensemble de règles de principal, de portée et de préfixe de ressource.",
  "access.principal2": "Principal",
  "access.format": "Format",
  "access.permission": "Permission",
  "access.elevated": "Privilégiée",
  "access.resourcePrefix": "Préfixe de ressource",
  "access.edit2": "Modifier →",
  "access.repositoryGrants": "{filtered} autorisations de dépôt",
  "access.clear": "Effacer",
  "access.usernameApiKeyOrActor": "Nom d’utilisateur, clé d’API ou acteur",
  "access.repositoryName": "Nom du dépôt",
  "access.permissionLevel": "Niveau de permission",
  "access.allPermissions": "Toutes les permissions",
  "access.readBrowsePull": "Lecture · parcourir / extraire",
  "access.writePublishEdit": "Écriture · publier / modifier",
  "access.adminGrantDelete": "Admin · autoriser / supprimer",
  "access.noRepositoryGrants": "Aucune autorisation de dépôt",
  "access.noMatchingGrants": "Aucune autorisation correspondante",
  "access.addAPrincipalFromARepositorySAccessGrants":
    "Ajoutez un principal depuis l’onglet des autorisations d’accès d’un dépôt pour l’afficher ici",
  "access.tryAnotherFilter": "Essayez un autre filtre",
  "access.oidcCustom": "OIDC / personnalisé",
  "access.publicAccessBoundary": "Limite d’accès public",
  "access.repositoriesPublic":
    "{publicRepositoryCount} dépôts publics sur {repositoryCount}",
  "access.disableGlobalAnonymousReads":
    "Désactiver les lectures anonymes globales?",
  "access.enableGlobalAnonymousReads":
    "Activer les lectures anonymes globales?",
  "access.allUnauthenticatedProtocolReadsWillBeRejected":
    "Toutes les lectures de protocole non authentifiées seront rejetées.",
  "access.artifactsAllowedByRepositoryOrGroupPolicyBecomeReadable":
    "Les artefacts autorisés par la politique du dépôt ou du groupe deviennent lisibles sans authentification.",
  "access.continue": "Continuer",
  "access.toggleGlobalAnonymousReads":
    "Basculer les lectures anonymes globales",
  "access.loading": "Chargement…",
  "access.globalGateOnReadOnly": "Barrière globale activée · Lecture seule",
  "access.globalGateOffReadOnly": "Barrière globale désactivée · Lecture seule",
  "access.readOnly": "Lecture seule",
  "access.anonymousReadsRequireGlobalRepositoryAndApplicableGroupOpt":
    "Les lectures anonymes exigent l’adhésion globale, celle du dépôt et celle du groupe applicable; les écritures, les suppressions et la gestion exigent toujours une authentification.",
  "access.templateNameAndEveryGrantPrincipalAreRequired":
    "Le nom du modèle et chaque principal d’autorisation sont obligatoires",
  "access.principalAndResourcePrefixPairsMustBeUnique":
    "Les paires principal et préfixe de ressource doivent être uniques",
  "access.failedToSaveAuthorizationTemplate":
    "Échec de l’enregistrement du modèle d’autorisation",
  "access.editAuthorizationTemplate": "Modifier le modèle d’autorisation",
  "access.newAuthorizationTemplate": "Nouveau modèle d’autorisation",
  "access.templateName": "Nom du modèle",
  "access.descriptionOptional": "Description (facultatif)",
  "access.chooseAKnownUserOrApiKeyOrEnter":
    "Choisissez un utilisateur ou une clé d’API connus, ou saisissez directement un acteur OIDC / CI. Laissez le préfixe de ressource vide pour tout le dépôt; les suggestions suivent le format du dépôt.",
  "access.chooseOrEnterAnActorEGUserRelease":
    "Choisissez ou saisissez un acteur, p. ex. user:release",
  "access.scopes": "Portées",
  "access.customPermissions": "Permissions personnalisées",
  "access.admin2": "Admin",
  "access.removeRule": "Supprimer la règle",
  "access.addRule": "Ajouter une règle",
  "access.allOperationsBrowsePublishDeleteGrantsAndKeyManagement":
    "Toutes les opérations\u00a0: parcourir, publier, supprimer, autorisations et gestion des clés",
  "access.noImplicitCapabilityRepositoryAccessComesOnlyFromPer":
    "Aucune capacité implicite\u00a0: l’accès aux dépôts provient uniquement des autorisations par dépôt",
  "access.awaitingApprovalNoRepositoryPathIsReachable":
    "En attente d’approbation\u00a0: aucun chemin de dépôt n’est accessible",
  "access.identifyThePrincipal": "Identifier le principal",
  "access.administratorIdentitiesPassDirectlyUsersApiKeysAndOidc":
    "Les identités d’administrateur passent directement. Les utilisateurs, les clés d’API et les identités OIDC entrent avec leur rôle global.",
  "access.applyTheGlobalRole": "Appliquer le rôle global",
  "access.adminAllowsAllOperationsIncludingArtifactIntelligenceWritesA":
    "Admin autorise toutes les opérations, y compris les écritures d’intelligence des artefacts. Un niveau member n’apporte aucune capacité sur les dépôts; les autorisations par dépôt décident, et l’intelligence des artefacts est une portée indépendante de moindre privilège.",
  "access.applyRepositoryRules": "Appliquer les règles de dépôt",
  "access.thePrincipalPermissionAndResourcePrefixMustAllMatch":
    "Le principal, la permission et le préfixe de ressource doivent tous correspondre. L’intelligence des artefacts n’écrit que des métadonnées de sécurité; sinon l’accès est refusé.",
  "access.legacyCompatibility": "Compatibilité héritée",
  "access.unmanagedRepositoriesCanFallBackToLegacyStaticPolicy":
    "Les dépôts non gérés peuvent se rabattre sur la politique statique héritée. L’accès anonyme est évalué séparément.",
  "access.principals": "Principals",
  "access.usersApiKeysOrOtherPrincipals":
    "Utilisateurs, clés d’API ou autres principals",
  "access.writeGrants": "Autorisations d’écriture",
  "access.canPublishOrEditArtifacts":
    "Peuvent publier ou modifier des artefacts",
  "access.adminGrants": "Autorisations d’administration",
  "access.canManageGrantsAndDelete":
    "Peuvent gérer les autorisations et supprimer",
  "access.scopeIntelligence": "Intelligence des artefacts",
  "access.scopeAdmin": "Admin",
  "access.scopeWrite": "Écriture",
  "access.scopeRead": "Lecture",
  "access.scopeUnknown": "Non configuré",
  "access.administratorIdentity": "Identité d’administrateur",
  "access.legacyStaticPolicy": "Politique statique héritée",
  "access.anonymousAccessPolicy": "Politique d’accès anonyme",
  "access.repositoryGrant": "Autorisation de dépôt",
  "access.notSpecified": "Non précisé",
  "access.allowedByAdministratorIdentity":
    "Autorisé par l’identité d’administrateur",
  "access.allowedByTheGlobalAdminRole": "Autorisé par le rôle global admin",
  "access.aMemberLevelCarriesNoImplicitCapabilityRepository":
    "Un niveau member n’apporte aucune capacité implicite; les autorisations de dépôt décident",
  "access.principalPermissionAndResourceScopeAllMatch":
    "Le principal, la permission et la portée de ressource correspondent tous",
  "access.noRepositoryRuleMatchesThePrincipalPermissionAnd":
    "Aucune règle de dépôt ne correspond au principal, à la permission et à la portée de ressource",
  "access.repositoryGrantsCouldNotBeLoadedAccessIs":
    "Les autorisations de dépôt n’ont pas pu être chargées; l’accès est refusé",
  "access.matchedALegacyReadRule": "Correspond à une règle de lecture héritée",
  "access.matchedALegacyWriteRule": "Correspond à une règle d’écriture héritée",
  "access.globalAnonymousReadsAreDisabled":
    "Les lectures anonymes globales sont désactivées",
  "access.anonymousReadsAreDisabledForThisRepository":
    "Les lectures anonymes sont désactivées pour ce dépôt",
  "access.anonymousReadsAreEnabledGloballyAndForThis":
    "Les lectures anonymes sont activées globalement et pour ce dépôt",
  "access.theRepositoryIsNotCurrentlyActive":
    "Le dépôt n’est pas actif pour le moment",
  "access.allowed": "Autorisé",
  "access.denied": "Refusé",
  "access.anonymousReads": "Lectures anonymes",
  "access.artifactIdOptional": "artifactId (facultatif)",
  "access.namespaceTeam": "Namespace · team",
  "access.imageNameOptional": "Nom de l’image (facultatif)",
  "access.name": "Nom",
  "access.versionOptional": "Version (facultatif)",
  "access.userOptional": "Utilisateur (facultatif)",
  "access.channelOptional": "Canal (facultatif)",
  "access.scopeTeam": "Portée · @team",
  "access.packageOptional": "Paquet (facultatif)",
  "access.pathPrefixReleases": "Préfixe de chemin · releases/",
  "access.projectPrefixGateway": "Préfixe de projet · gateway-",
  "access.modulePathPrefixExampleComTeam":
    "Préfixe de chemin de module · example.com/team/",
  "access.repositoryPathPrefixDistsBookworm":
    "Préfixe de chemin du dépôt · dists/bookworm/",
  "access.limitByPrefix": "Limiter par préfixe",
  "access.canonicalPrefix": "Préfixe canonique",
  "access.readWriteAdminIntelligence":
    "Lecture + écriture + admin + intelligence",
  "access.readWrite": "Lecture + écriture",
  "access.noGlobalRoleRepositoryGrantsDecideAccess":
    "Aucun rôle global; les autorisations de dépôt décident de l’accès",
  "access.mustExactlyMatchTheAuthenticatedActor":
    "Doit correspondre exactement à l’acteur authentifié",
  "access.selectAUserAPIKeyServiceAccountOr":
    "Sélectionnez un utilisateur, une clé d’API, un compte de service ou une identité externe",
  "access.users": "Utilisateurs",
  "access.serviceAccounts": "Comptes de service",
  "access.externalIdentities": "Identités externes",
  "access.principalIdentifier": "Identifiant du principal",
  "access.completeActorForExampleOidcGitlabTeamRelease":
    "Acteur complet, par exemple oidc:gitlab:team/release",
  "access.permission2": "Permission",
  "access.savedPermissionSnapshot": "Instantané de permissions enregistré",
  "access.builtInPermissions": "Permissions intégrées",
  "access.adminGrantDelete2": "Admin · autoriser / supprimer",
  "access.artifactIntelligenceSecurityMetadata":
    "Intelligence des artefacts · métadonnées de sécurité",
  "access.customRoles": "Rôles personnalisés",
  "access.grantedByThisRule": "Accordé par cette règle",
  "access.resourceScope": "Portée de ressource",
};
